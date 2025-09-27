package session_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/xkilldash9x/scalpel-cli/internal/browser/session"
)

// Mock transport to simulate network behavior
type mockTransport struct {
	handler func(req *http.Request) (*http.Response, error)
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if m.handler != nil {
		return m.handler(req)
	}
	return nil, http.ErrNotSupported
}

func TestHarvester_RoundTrip_Capture(t *testing.T) {
	logger := zap.NewNop()
	requestBody := "Test Request Body"
	responseBody := "Test Response Body"

	transport := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			// Verify request body is still readable by the transport (ensures Harvester restores it)
			reqBodyBytes, _ := io.ReadAll(req.Body)
			assert.Equal(t, requestBody, string(reqBodyBytes))

			return &http.Response{
				StatusCode: http.StatusOK,
				Proto:      "HTTP/1.1",
				Header:     http.Header{"Content-Type": []string{"text/plain"}},
				Body:       io.NopCloser(strings.NewReader(responseBody)),
				Request:    req,
			}, nil
		},
	}

	harvester := session.NewHarvester(transport, logger, true) // Enable body capture
	client := &http.Client{Transport: harvester}

	// 1. Execute Request
	req, _ := http.NewRequest("POST", "http://example.com/data?q=1", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "text/plain")

	resp, err := client.Do(req)
	require.NoError(t, err)

	// 2. Consume Response Body (Crucial for Harvester wrapper to finalize recording)
	respBodyBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, responseBody, string(respBodyBytes))

	// 3. Generate and Verify HAR
	har := harvester.GenerateHAR()
	require.Len(t, har.Log.Entries, 1)
	entry := har.Log.Entries[0]

	// Verify Request details
	assert.Equal(t, "POST", entry.Request.Method)
	assert.Equal(t, "http://example.com/data?q=1", entry.Request.URL)
	require.NotNil(t, entry.Request.PostData)
	assert.Equal(t, requestBody, entry.Request.PostData.Text)
	require.Len(t, entry.Request.QueryString, 1)
	assert.Equal(t, "q", entry.Request.QueryString[0].Name)

	// Verify Response details
	assert.Equal(t, http.StatusOK, entry.Response.Status)
	assert.Equal(t, responseBody, entry.Response.Content.Text)
}

func TestHarvester_RoundTrip_BinaryEncoding(t *testing.T) {
	logger := zap.NewNop()
	// Binary data (e.g., PNG header)
	responseData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	expectedBase64 := "iVBORw0KGgo="

	transport := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"image/png"}},
				Body:       io.NopCloser(bytes.NewReader(responseData)),
				Request:    req,
			}, nil
		},
	}

	harvester := session.NewHarvester(transport, logger, true)
	client := &http.Client{Transport: harvester}

	resp, err := client.Get("http://example.com/image.png")
	require.NoError(t, err)
	// Consume and close the body
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	har := harvester.GenerateHAR()
	require.Len(t, har.Log.Entries, 1)
	entry := har.Log.Entries[0]

	// Verify encoding for binary content
	assert.Equal(t, "base64", entry.Response.Content.Encoding)
	assert.Equal(t, expectedBase64, entry.Response.Content.Text)
}

// Helper struct for testing WaitNetworkIdle synchronization
type delayCloseBody struct {
	io.Reader
	closeSignal chan struct{}
}

func (d *delayCloseBody) Close() error {
	<-d.closeSignal // Wait for the signal before closing
	return nil
}

func TestHarvester_WaitNetworkIdle(t *testing.T) {
	logger := zap.NewNop()
	// Use channels to precisely control the timing of the mock transport and body consumption
	startTransport := make(chan struct{})
	finishBodyRead := make(chan struct{})

	transport := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			<-startTransport // Wait until signaled to start the transport phase
			// Simulate network latency
			time.Sleep(50 * time.Millisecond)

			// Return a response with a body that waits before closing (body consumption phase)
			body := &delayCloseBody{
				Reader:      strings.NewReader("data"),
				closeSignal: finishBodyRead,
			}
			return &http.Response{StatusCode: http.StatusOK, Body: body, Request: req}, nil
		},
	}

	harvester := session.NewHarvester(transport, logger, false)
	client := &http.Client{Transport: harvester}

	// 1. Start the request lifecycle in a goroutine
	go func() {
		resp, err := client.Get("http://example.com/async")
		if err == nil {
			// Consume the body
			io.ReadAll(resp.Body)
			resp.Body.Close() // This will block until finishBodyRead is signaled
		}
	}()

	// 2. Setup WaitNetworkIdle monitoring
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	idleDone := make(chan error)
	quietPeriod := 100 * time.Millisecond

	go func() {
		idleDone <- harvester.WaitNetworkIdle(ctx, quietPeriod)
	}()

	// Ensure it's waiting (Harvester knows about the request, but transport hasn't started)
	select {
	case <-idleDone:
		t.Fatal("WaitNetworkIdle returned before the request transport phase started")
	case <-time.After(50 * time.Millisecond):
		// Expected: still waiting
	}

	// 3. Signal transport to start processing
	close(startTransport)

	// Ensure it's waiting while the request is in flight (transport phase and body consumption phase)
	select {
	case <-idleDone:
		t.Fatal("WaitNetworkIdle returned while request was in flight")
	case <-time.After(100 * time.Millisecond): // Wait longer than the simulated transport latency
		// Expected: still waiting
	}

	// 4. Signal body consumption to finish (request lifecycle truly ends)
	close(finishBodyRead)

	// 5. WaitNetworkIdle should now wait for the quiet period (100ms) and then return
	startTime := time.Now()
	select {
	case err := <-idleDone:
		require.NoError(t, err)
		duration := time.Since(startTime)
		// It should take at least the quiet period time after the request finished
		assert.GreaterOrEqual(t, duration, quietPeriod, "WaitNetworkIdle returned too quickly")
	case <-ctx.Done():
		t.Fatal("WaitNetworkIdle timed out waiting for completion")
	}
}

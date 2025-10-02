package session

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	"go.uber.org/zap"
)

// The interval at which we check for network idleness.
const networkCheckInterval = 100 * time.Millisecond

// Harvester is an http.RoundTripper middleware. It wraps another transport
// to capture HTTP requests and responses, compiling them into a HAR format.
// It also tracks in-flight requests to determine when network activity has settled.
type Harvester struct {
	transport      http.RoundTripper
	logger         *zap.Logger
	captureContent bool

	// mu protects access to the shared state below.
	mu             sync.Mutex
	entries        []schemas.Entry
	activeRequests int
	lastActivity   time.Time
}

// NewHarvester spins up a new Harvester middleware.
// If the provided transport is nil, it defaults to http.DefaultTransport.
func NewHarvester(transport http.RoundTripper, logger *zap.Logger, captureContent bool) *Harvester {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &Harvester{
		transport:      transport,
		logger:         logger,
		captureContent: captureContent,
		lastActivity:   time.Now(),
		entries:        make([]schemas.Entry, 0),
	}
}

// RoundTrip executes the request, tracks activity, and wraps the response body
// to ensure the HAR entry is finalized when the body is consumed by the caller.
func (h *Harvester) RoundTrip(req *http.Request) (*http.Response, error) {
	// 1. Read the request body (non-destructively).
	requestBody := readAndRestoreBody(&req.Body, h.logger, "request")

	// 2. Start tracking activity.
	h.trackActivity(true)
	startTime := time.Now()

	// 3. Pass the request to the next transport in the chain.
	resp, err := h.transport.RoundTrip(req)

	if err != nil {
		// 4a. If transport failed (e.g., timeout before headers, connection error), stop tracking and record immediately.
		h.trackActivity(false)
		duration := time.Since(startTime)

		// Record the entry even on error (resp might be non-nil if headers were received).
		h.recordEntry(req, resp, startTime, duration, requestBody, nil)
		return resp, err
	}

	// 4b. If transport succeeded, wrap the response body.
	// The wrapper ensures that we finalize the HAR entry (including total duration and activity tracking)
	// only when the consumer closes the body.

	wrapper := newBodyWrapper(resp.Body, h.captureContent, func(responseBody []byte, totalDuration time.Duration) {
		// Callback executed when the body is closed.
		h.trackActivity(false)
		h.recordEntry(req, resp, startTime, totalDuration, requestBody, responseBody)
	}, startTime)

	resp.Body = wrapper

	return resp, nil
}

// -- Body Wrapper --

// bodyWrapper wraps an io.ReadCloser. It captures the body content as it's read
// and executes a callback when Close is called.
type bodyWrapper struct {
	originalBody   io.ReadCloser
	captureContent bool
	onClose        func(responseBody []byte, totalDuration time.Duration)
	buffer         bytes.Buffer
	startTime      time.Time
	// closed ensures the onClose callback runs exactly once.
	closed int32
}

func newBodyWrapper(body io.ReadCloser, capture bool, onClose func([]byte, time.Duration), startTime time.Time) *bodyWrapper {
	return &bodyWrapper{
		originalBody:   body,
		captureContent: capture,
		onClose:        onClose,
		startTime:      startTime,
	}
}

// Read reads from the original body and buffers the content if capture is enabled.
func (bw *bodyWrapper) Read(p []byte) (int, error) {
	n, err := bw.originalBody.Read(p)
	if n > 0 && bw.captureContent {
		// In case of error during write to buffer (unlikely for bytes.Buffer), we proceed.
		bw.buffer.Write(p[:n])
	}
	// err might be io.EOF or a real error (like timeout/context canceled).
	return n, err
}

// Close closes the original body and executes the finalization callback.
func (bw *bodyWrapper) Close() error {
	// Ensure the original body is closed.
	err := bw.originalBody.Close()

	// Execute the callback exactly once.
	if atomic.CompareAndSwapInt32(&bw.closed, 0, 1) {
		duration := time.Since(bw.startTime)
		var bodyBytes []byte
		if bw.captureContent {
			bodyBytes = bw.buffer.Bytes()
		}

		if bw.onClose != nil {
			bw.onClose(bodyBytes, duration)
		}
	}
	return err
}

// trackActivity bumps the active request counter up or down.
// This is used to figure out when the network is quiet.
func (h *Harvester) trackActivity(start bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if start {
		h.activeRequests++
	} else {
		h.activeRequests--
		// This should technically never go below zero, but let's be safe.
		if h.activeRequests < 0 {
			h.activeRequests = 0
		}
	}
	h.lastActivity = time.Now()
}

// WaitNetworkIdle blocks until there are no in-flight requests and a
// specified quiet period has passed since the last activity.
func (h *Harvester) WaitNetworkIdle(ctx context.Context, quietPeriod time.Duration) error {
	h.logger.Debug("Waiting for network to become idle.")
	ticker := time.NewTicker(networkCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			h.logger.Warn("Wait for network idle cancelled or timed out.", zap.Error(ctx.Err()))
			return ctx.Err()
		case <-ticker.C:
			h.mu.Lock()
			active := h.activeRequests
			sinceLastActivity := time.Since(h.lastActivity)
			h.mu.Unlock()

			// The network is idle if nothing is happening right now, AND
			// a sufficient amount of time has passed since the last thing finished.
			if active == 0 && sinceLastActivity >= quietPeriod {
				h.logger.Debug("Network idle condition met.")
				return nil
			}
		}
	}
}

// recordEntry builds a HAR entry from a completed transaction and saves it.
func (h *Harvester) recordEntry(req *http.Request, resp *http.Response, start time.Time, duration time.Duration, reqBody, respBody []byte) {
	// resp might be nil if the transport failed completely (e.g., DNS error).
	if resp == nil {
		return
	}

	entry := schemas.Entry{
		StartedDateTime: start,
		Time:            float64(duration.Milliseconds()),
		Request:         h.buildRequestObject(req, reqBody),
		Response:        h.buildResponseObject(resp, respBody),
		Timings: schemas.Timings{
			// The total duration is a decent approximation for the 'wait' time
			// in the context of a client side HAR.
			Wait: float64(duration.Milliseconds()),
		},
		Cache: struct{}{}, // Not implemented, but required by HAR spec.
	}

	h.mu.Lock()
	h.entries = append(h.entries, entry)
	h.mu.Unlock()
}

// GenerateHAR creates the final HAR structure from all captured entries.
// This returns a copy of the entries, so the harvester can continue capturing
// new traffic without affecting the generated result.
func (h *Harvester) GenerateHAR() *schemas.HAR {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Create a copy to avoid race conditions if the caller modifies the slice.
	entriesCopy := make([]schemas.Entry, len(h.entries))
	copy(entriesCopy, h.entries)

	har := schemas.NewHAR()
	har.Log.Entries = entriesCopy
	return har
}

// -- HAR Object Builders --

// builds the HAR Request object from an http.Request and its body.
func (h *Harvester) buildRequestObject(req *http.Request, body []byte) schemas.Request {
	request := schemas.Request{
		Method:      req.Method,
		URL:         req.URL.String(),
		HTTPVersion: req.Proto,
		Headers:     mapToNVPair(req.Header),
		QueryString: mapToQueryNVPair(req.URL.Query()),
		BodySize:    int64(len(body)),
		HeadersSize: -1, // Not easily available in net/http, -1 is standard for unknown.
	}

	if len(body) > 0 {
		contentType := req.Header.Get("Content-Type")
		request.PostData = &schemas.PostData{
			MimeType: contentType,
			// For requests, we assume the body is text. Binary uploads are less common
			// and would likely be multipart, which this simple HAR doesn't fully model.
			Text: string(body),
		}
	}
	return request
}

// builds the HAR Response object from an http.Response and its body.
func (h *Harvester) buildResponseObject(resp *http.Response, body []byte) schemas.Response {
	response := schemas.Response{
		Status:      resp.StatusCode,
		StatusText:  http.StatusText(resp.StatusCode),
		HTTPVersion: resp.Proto,
		Headers:     mapToNVPair(resp.Header),
		RedirectURL: resp.Header.Get("Location"),
		BodySize:    int64(len(body)),
		HeadersSize: -1, // Not easily available in net/http, -1 is standard for unknown.
	}

	// If body content wasn't captured (len(body) == 0), use Content-Length if available for BodySize.
	if response.BodySize == 0 && resp.ContentLength > 0 {
		response.BodySize = resp.ContentLength
	}

	contentType := resp.Header.Get("Content-Type")
	response.Content.Size = int64(len(body))
	response.Content.MimeType = contentType

	if h.captureContent && len(body) > 0 {
		if isTextMime(contentType) {
			response.Content.Text = string(body)
		} else {
			response.Content.Encoding = "base64"
			response.Content.Text = base64.StdEncoding.EncodeToString(body)
		}
	}
	return response
}

// -- Utility Functions --

// Reads all bytes from an io.ReadCloser and then replaces it with a new
// reader containing the same bytes, allowing it to be read again.
// Used primarily for the request body.
func readAndRestoreBody(body *io.ReadCloser, logger *zap.Logger, entity string) []byte {
	if body == nil || *body == nil || *body == http.NoBody {
		return nil
	}

	data, err := io.ReadAll(*body)
	if err != nil {
		logger.Warn("Failed to read body for HAR capture.",
			zap.String("entity", entity),
			zap.Error(err),
		)
		// The original body is now in a bad state. Replace it with a reader
		// on what we managed to get, which might be nothing.
		*body = io.NopCloser(bytes.NewReader(data))
		return data
	}

	// Replace the now consumed body with a new reader on the captured data.
	*body = io.NopCloser(bytes.NewReader(data))
	return data
}

// checks if a MIME type is likely to be human readable text.
func isTextMime(mimeType string) bool {
	lowerMime := strings.ToLower(mimeType)
	return strings.HasPrefix(lowerMime, "text/") ||
		strings.Contains(lowerMime, "javascript") ||
		strings.Contains(lowerMime, "json") ||
		strings.Contains(lowerMime, "xml") ||
		strings.Contains(lowerMime, "svg")
}

// converts http.Header to the HAR NVPair schema.
func mapToNVPair(h http.Header) []schemas.NVPair {
	pairs := make([]schemas.NVPair, 0, len(h))
	for k, values := range h {
		for _, v := range values {
			pairs = append(pairs, schemas.NVPair{Name: k, Value: v})
		}
	}
	return pairs
}

// converts url.Values to the HAR NVPair schema.
func mapToQueryNVPair(q url.Values) []schemas.NVPair {
	pairs := make([]schemas.NVPair, 0, len(q))
	for k, values := range q {
		for _, v := range values {
			pairs = append(pairs, schemas.NVPair{Name: k, Value: v})
		}
	}
	return pairs
}
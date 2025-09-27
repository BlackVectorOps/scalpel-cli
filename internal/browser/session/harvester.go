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
	"time"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	"go.uber.org/zap"
)

// Harvester captures network traffic and monitors activity.
type Harvester struct {
	transport      http.RoundTripper
	logger         *zap.Logger
	captureContent bool

	mu             sync.Mutex
	entries        []schemas.Entry
	activeRequests int
	lastActivity   time.Time
}

// NewHarvester creates a new Harvester middleware.
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

// RoundTrip executes the request and records the transaction.
func (h *Harvester) RoundTrip(req *http.Request) (*http.Response, error) {
	h.trackActivity(true)
	defer h.trackActivity(false)

	startTime := time.Now()

	var requestBody []byte
	if req.Body != nil && req.ContentLength > 0 {
		var err error
		requestBody, err = io.ReadAll(req.Body)
		if err != nil {
			h.logger.Warn("Failed to read request body for HAR.", zap.Error(err))
		}
		req.Body = io.NopCloser(bytes.NewBuffer(requestBody))
	}

	resp, err := h.transport.RoundTrip(req)
	duration := time.Since(startTime)

	if err != nil {
		return resp, err
	}

	var responseBody []byte
	if resp.Body != nil {
		var readErr error
		responseBody, readErr = io.ReadAll(resp.Body)
		if readErr != nil {
			h.logger.Warn("Failed to read response body.", zap.Error(readErr))
		}
		resp.Body = io.NopCloser(bytes.NewBuffer(responseBody))
	}

	h.recordEntry(req, resp, startTime, duration, requestBody, responseBody)

	return resp, nil
}

// trackActivity updates the state of active requests for stabilization.
func (h *Harvester) trackActivity(start bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if start {
		h.activeRequests++
	} else {
		h.activeRequests--
		if h.activeRequests < 0 {
			h.activeRequests = 0
		}
	}
	h.lastActivity = time.Now()
}

// WaitNetworkIdle waits until there are no active requests and a quiet period has passed.
func (h *Harvester) WaitNetworkIdle(ctx context.Context, quietPeriod time.Duration) error {
	h.logger.Debug("Waiting for network idle.")
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			h.mu.Lock()
			active := h.activeRequests
			sinceLastActivity := time.Since(h.lastActivity)
			h.mu.Unlock()

			if active == 0 && sinceLastActivity >= quietPeriod {
				h.logger.Debug("Network idle reached.")
				return nil
			}
		}
	}
}

// recordEntry maps data to a HAR Entry structure.
func (h *Harvester) recordEntry(req *http.Request, resp *http.Response, start time.Time, duration time.Duration, reqBody, respBody []byte) {
	entry := schemas.Entry{
		StartedDateTime: start,
		Time:            float64(duration.Milliseconds()),
		Request: schemas.Request{
			Method:      req.Method,
			URL:         req.URL.String(),
			HTTPVersion: req.Proto,
			Headers:     mapToNVPair(req.Header),
			QueryString: mapToQueryNVPair(req.URL.Query()),
			BodySize:    int64(len(reqBody)),
			HeadersSize: -1, // Not easily available in net/http
		},
		Response: schemas.Response{
			Status:      resp.StatusCode,
			StatusText:  http.StatusText(resp.StatusCode),
			HTTPVersion: resp.Proto,
			Headers:     mapToNVPair(resp.Header),
			RedirectURL: resp.Header.Get("Location"),
			HeadersSize: -1, // Not easily available in net/http
		},
		Timings: schemas.Timings{
			Wait: float64(duration.Milliseconds()),
		},
		Cache: struct{}{},
	}

	if len(reqBody) > 0 {
		contentType := req.Header.Get("Content-Type")
		entry.Request.PostData = &schemas.PostData{
			MimeType: contentType,
			Text:     string(reqBody),
		}
	}

	respBodySize := int64(len(respBody))
	contentType := resp.Header.Get("Content-Type")

	entry.Response.BodySize = respBodySize
	entry.Response.Content.Size = respBodySize
	entry.Response.Content.MimeType = contentType

	if h.captureContent && respBodySize > 0 {
		if isTextMime(contentType) {
			entry.Response.Content.Text = string(respBody)
		} else {
			entry.Response.Content.Encoding = "base64"
			entry.Response.Content.Text = base64.StdEncoding.EncodeToString(respBody)
		}
	}

	h.mu.Lock()
	h.entries = append(h.entries, entry)
	h.mu.Unlock()
}

// GenerateHAR creates the final HAR structure.
func (h *Harvester) GenerateHAR() *schemas.HAR {
	h.mu.Lock()
	defer h.mu.Unlock()

	entriesCopy := make([]schemas.Entry, len(h.entries))
	copy(entriesCopy, h.entries)

	har := schemas.NewHAR()
	har.Log.Entries = entriesCopy
	return har
}

// isTextMime checks if a MIME type is likely text-based.
func isTextMime(mimeType string) bool {
	lowerMime := strings.ToLower(mimeType)
	return strings.HasPrefix(lowerMime, "text/") ||
		strings.Contains(lowerMime, "javascript") ||
		strings.Contains(lowerMime, "json") ||
		strings.Contains(lowerMime, "xml")
}

// mapToNVPair converts http.Header to the HAR NVPair schema.
func mapToNVPair(h http.Header) []schemas.NVPair {
	pairs := make([]schemas.NVPair, 0, len(h))
	for k, values := range h {
		for _, v := range values {
			pairs = append(pairs, schemas.NVPair{Name: k, Value: v})
		}
	}
	return pairs
}

// mapToQueryNVPair converts url.Values to the HAR NVPair schema.
func mapToQueryNVPair(q url.Values) []schemas.NVPair {
	pairs := make([]schemas.NVPair, 0, len(q))
	for k, values := range q {
		for _, v := range values {
			pairs = append(pairs, schemas.NVPair{Name: k, Value: v})
		}
	}
	return pairs
}

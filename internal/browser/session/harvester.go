package session

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"github.com/xkilldash9x/scalpel-cli/api/schemas"
)

// Harvester captures network traffic and monitors activity.
type Harvester struct {
	transport *http.RoundTripper // Use a pointer for the interface to allow easy replacement
	logger *zap.Logger
	captureContent bool

	mu sync.Mutex
	entries []schemas.HAREntry
	activeRequests int
	lastActivity time.Time
}

// NewHarvester creates a new Harvester middleware.
func NewHarvester(transport http.RoundTripper, logger *zap.Logger, captureContent bool) *Harvester {
	if transport == nil {
		transport = http.DefaultTransport
	}
	// Note: We store a pointer to the RoundTripper interface to be consistent with common patterns.
	// You may want to just store the interface directly, but this aligns with a common Go RoundTripper pattern.
	rt := transport
	return &Harvester{
		transport: &rt,
		logger: logger,
		captureContent: captureContent,
		lastActivity: time.Now(),
		entries: make([]schemas.HAREntry, 0),
	}
}

// RoundTrip executes the request and records the transaction.
func (h *Harvester) RoundTrip(req *http.Request) (*http.Response, error) {
	h.trackActivity(true)
	defer h.trackActivity(false)

	startTime := time.Now()

	// Capture request body (Playwright logic integration)
	var requestBody []byte
	if req.Body != nil && req.ContentLength > 0 {
		var err error
		requestBody, err = io.ReadAll(req.Body)
		if err != nil {
			h.logger.Warn("Failed to read request body for HAR.", zap.Error(err))
		}
		// Restore the body for the actual transport.
		req.Body = io.NopCloser(bytes.NewBuffer(requestBody))
	}

	// Execute the request.
	resp, err := (*h.transport).RoundTrip(req)
	duration := time.Since(startTime)

	if err != nil {
		// Log a HAR entry for a failed request (optional, but good for debugging)
		// For simplicity, we only record successful RoundTrip in this version, as full error HAR is complex.
		return resp, err
	}

	// Capture response body (Playwright logic integration)
	var responseBody []byte
	if resp.Body != nil {
		// Always read the body to track size and ensure the stream is processed.
		var readErr error
		responseBody, readErr = io.ReadAll(resp.Body)
		if readErr != nil {
			h.logger.Warn("Failed to read response body.", zap.Error(readErr))
		}
		// Restore the body for the consumer (Session/other middleware).
		resp.Body = io.NopCloser(bytes.NewBuffer(responseBody))
	}

	// Record the entry.
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

// recordEntry maps data to a HAR Entry structure, incorporating body content logic from Playwright.
func (h *Harvester) recordEntry(req *http.Request, resp *http.Response, start time.Time, duration time.Duration, reqBody, respBody []byte) {
	entry := schemas.HAREntry{
		StartedDateTime: start.Format(time.RFC3339Nano),
		Time: float64(duration.Milliseconds()),
		Request: schemas.HARRequest{
			Method: req.Method,
			URL: req.URL.String(),
			HTTPVersion: req.Proto,
			Headers: mapToHARHeaders(req.Header),
			QueryString: mapToHARQuery(req.URL.Query()),
			BodySize: int64(len(reqBody)),
			// Note: Playwright logic for HeadersSize is not fully replicated as Go's http.Request doesn't easily provide raw header size.
			HeadersSize: -1, 
		},
		Response: schemas.HARResponse{
			Status: resp.StatusCode,
			StatusText: http.StatusText(resp.StatusCode),
			HTTPVersion: resp.Proto,
			Headers: mapToHARHeaders(resp.Header),
			RedirectURL: resp.Header.Get("Location"),
			HeadersSize: -1, // Same as above, not easily available
		},
		Timings: schemas.HARTimings{
			// Simplified timing model as net/http RoundTripper doesn't expose detailed timings.
			Wait: float64(duration.Milliseconds()), 
		},
		Cache: schemas.HARCache{}, // Empty cache structure for compliance
	}

	// 1. Request PostData (Playwright logic integration)
	if len(reqBody) > 0 {
		contentType := req.Header.Get("Content-Type")
		entry.Request.PostData = &schemas.HARPostData{
			MimeType: contentType,
			Text: string(reqBody),
		}
	}

	// 2. Response Content Encoding (Playwright logic integration)
	respBodySize := int64(len(respBody))
	contentType := resp.Header.Get("Content-Type")

	entry.Response.BodySize = respBodySize
	entry.Response.Content.Size = respBodySize
	entry.Response.Content.MimeType = contentType

	if h.captureContent && respBodySize > 0 {
		if isTextMime(contentType) {
			entry.Response.Content.Text = string(respBody)
		} else {
			// For binary content, encode to base64 for HAR format compatibility.
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

	entriesCopy := make([]schemas.HAREntry, len(h.entries))
	copy(entriesCopy, h.entries)

	return &schemas.HAR{
		Log: schemas.HARLog{
			Version: "1.2",
			Creator: schemas.HARCreator{
				Name: "CustomAutomationBrowser (PureGo)",
				Version: "1.0",
			},
			Entries: entriesCopy,
			// For a pure Go HTTP client, the "Pages" array is typically omitted or simplified.
			// If you add a "page" concept, you'd populate it here.
		},
	}
}

// isTextMime is pulled directly from the Playwright version's helper.
func isTextMime(mimeType string) bool {
	lowerMime := strings.ToLower(mimeType)
	return strings.HasPrefix(lowerMime, "text/") ||
		strings.Contains(lowerMime, "javascript") ||
		strings.Contains(lowerMime, "json") ||
		strings.Contains(lowerMime, "xml")
}

// mapToHARHeaders converts http.Header to the HAR schema format.
func mapToHARHeaders(h http.Header) []schemas.HARHeader {
	pairs := make([]schemas.HARHeader, 0, len(h))
	for k, values := range h {
		for _, v := range values {
			// Headers like "Set-Cookie" can have multiple values, which must be separate entries in HAR.
			pairs = append(pairs, schemas.HARHeader{Name: k, Value: v})
		}
	}
	return pairs
}

// mapToHARQuery converts url.Values to the HAR QueryString schema.
func mapToHARQuery(q url.Values) []schemas.HARQueryString {
	pairs := make([]schemas.HARQueryString, 0, len(q))
	for k, values := range q {
		for _, v := range values {
			pairs = append(pairs, schemas.HARQueryString{Name: k, Value: v})
		}
	}
	return pairs
}

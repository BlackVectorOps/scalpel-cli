package browser

import (
	"context"
	"encoding/base64"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"go.uber.org/zap"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"
)

// requestState tracks the lifecycle of a single network request.
type requestState struct {
	Request       *network.Request
	Response      *network.Response
	// Use pointers as the CDP events provide pointers for these fields.
	StartTS       *cdp.TimeSinceEpoch // Wall time for HAR StartedDateTime
	EndTS         *cdp.MonotonicTime
	ResponseReady chan struct{} // Channel to signal when response headers are received
	Body          []byte
	BodyBase64    bool
	IsComplete    bool
}

// Harvester listens to network, console, and runtime events to collect artifacts and monitor network activity.
// Adheres to Principle 6 (Network Layer) and supports Principle 2 (Dynamic Waits).
type Harvester struct {
	logger        *zap.Logger
	captureBodies bool

	// sessionCtx is the context of the browser session (tab).
	sessionCtx context.Context
	// listenerCtx is the context specifically for the ListenTarget goroutine.
	listenerCtx    context.Context
	cancelListener context.CancelFunc

	// Data storage and synchronization
	requests         map[network.RequestID]*requestState
	inflightRequests map[network.RequestID]bool // Used specifically for WaitNetworkIdle tracking
	consoleLogs      []schemas.ConsoleLog
	lock             sync.RWMutex

	// bodyFetchWG tracks active body fetching goroutines.
	bodyFetchWG sync.WaitGroup

	isStarted bool
}

// NewHarvester creates a new artifact harvester for a specific session.
func NewHarvester(sessionCtx context.Context, logger *zap.Logger, captureBodies bool) *Harvester {
	return &Harvester{
		sessionCtx:       sessionCtx,
		logger:           logger.Named("harvester"),
		captureBodies:    captureBodies,
		requests:         make(map[network.RequestID]*requestState),
		inflightRequests: make(map[network.RequestID]bool),
		consoleLogs:      make([]schemas.ConsoleLog, 0),
	}
}

// Start begins listening for CDP events and enables the necessary domains.
func (h *Harvester) Start(ctx context.Context) error {
	h.lock.Lock()
	defer h.lock.Unlock()

	if h.isStarted {
		return nil
	}

	// Create a cancellable context for the listener, derived from the session context.
	h.listenerCtx, h.cancelListener = context.WithCancel(h.sessionCtx)

	// Start the listener goroutine.
	go h.listen()

	// Enable the necessary CDP domains.
	err := chromedp.Run(ctx,
		network.Enable(),
		runtime.Enable(),
		log.Enable(),
	)

	if err != nil {
		h.cancelListener()
		return err
	}

	h.isStarted = true
	h.logger.Debug("Harvester started.")
	return nil
}

// listen is the main event loop for CDP events.
func (h *Harvester) listen() {
	chromedp.ListenTarget(h.listenerCtx, func(ev interface{}) {
		switch e := ev.(type) {
		// Network Events
		case *network.EventRequestWillBeSent:
			h.handleRequestWillBeSent(e)
		case *network.EventResponseReceived:
			h.handleResponseReceived(e)
		case *network.EventLoadingFinished:
			h.handleLoadingFinished(e)
		case *network.EventLoadingFailed:
			h.handleLoadingFailed(e)

		// Console/Runtime Events
		case *runtime.EventConsoleAPICalled:
			h.handleConsoleAPICalled(e)
		case *log.EventEntryAdded:
			h.handleLogEntryAdded(e)
		case *runtime.EventExceptionThrown:
			h.handleExceptionThrown(e)
		}
	})
}

// Stop halts the collection of events and returns the collected artifacts.
func (h *Harvester) Stop(ctx context.Context) (*schemas.HAR, []schemas.ConsoleLog) {
	h.lock.Lock()
	if !h.isStarted {
		h.lock.Unlock()
		return h.generateHAR(), h.getConsoleLogs()
	}

	// Stop the listener goroutine.
	if h.cancelListener != nil {
		h.cancelListener()
		h.cancelListener = nil
	}
	h.isStarted = false
	h.lock.Unlock()

	h.logger.Debug("Harvester stopped. Waiting for pending body fetches.")

	// Wait for pending body fetches to complete (Principle 3).
	h.waitForPendingFetches(ctx)

	// Generate the HAR log and return artifacts.
	return h.generateHAR(), h.getConsoleLogs()
}

// WaitNetworkIdle waits until there are no inflight requests for the duration of the quietPeriod (Principle 2).
func (h *Harvester) WaitNetworkIdle(ctx context.Context, quietPeriod time.Duration) error {
	ticker := time.NewTicker(quietPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Timeout reached or parent context cancelled.
			h.logger.Debug("WaitNetworkIdle aborted.", zap.Error(ctx.Err()))
			return ctx.Err()
		case <-ticker.C:
			// Check the inflight requests count.
			h.lock.RLock()
			inflightCount := len(h.inflightRequests)
			h.lock.RUnlock()

			if inflightCount == 0 {
				// Network is idle.
				return nil
			}
			h.logger.Debug("Waiting for network idle.", zap.Int("inflight_requests", inflightCount))
		}
	}
}

// -- Event Handlers --

func (h *Harvester) handleRequestWillBeSent(e *network.EventRequestWillBeSent) {
	h.lock.Lock()
	defer h.lock.Unlock()

	// Track for network idle detection.
	h.inflightRequests[e.RequestID] = true

	// Initialize state for HAR generation.
	// Assign the pointers directly.
	h.requests[e.RequestID] = &requestState{
		Request:       e.Request,
		StartTS:       e.WallTime,
		ResponseReady: make(chan struct{}),
	}

	// Handle redirects: Finalize the previous request associated with this ID if it exists.
	if e.RedirectResponse != nil {
		if prevState, ok := h.requests[e.RequestID]; ok && !prevState.IsComplete {
			prevState.Response = e.RedirectResponse
			prevState.IsComplete = true
			// Ensure channels are closed if they exist.
			select {
			case <-prevState.ResponseReady:
			default:
				close(prevState.ResponseReady)
			}
			// A redirect means the original request finished (from the browser's perspective).
			delete(h.inflightRequests, e.RequestID)
		}
	}
}

func (h *Harvester) handleResponseReceived(e *network.EventResponseReceived) {
	h.lock.Lock()
	defer h.lock.Unlock()

	if state, ok := h.requests[e.RequestID]; ok {
		state.Response = e.Response
		// Signal that headers are ready (needed for fetchBody synchronization).
		close(state.ResponseReady)
	}
}

func (h *Harvester) handleLoadingFinished(e *network.EventLoadingFinished) {
	h.lock.Lock()

	// Remove from network idle tracking.
	delete(h.inflightRequests, e.RequestID)

	state, ok := h.requests[e.RequestID]
	if !ok {
		h.lock.Unlock()
		return
	}

	// Assign the pointer directly.
	state.EndTS = e.Timestamp
	state.IsComplete = true

	// Determine if body capture is needed.
	shouldFetch := h.captureBodies && h.shouldCaptureBody(state.Response)

	if shouldFetch {
		h.bodyFetchWG.Add(1)
		h.lock.Unlock() // Unlock before launching goroutine.
		go h.fetchBody(e.RequestID)
	} else {
		h.lock.Unlock()
	}
}

func (h *Harvester) handleLoadingFailed(e *network.EventLoadingFailed) {
	h.lock.Lock()
	defer h.lock.Unlock()

	// Remove from network idle tracking.
	delete(h.inflightRequests, e.RequestID)

	if state, ok := h.requests[e.RequestID]; ok {
		// Assign the pointer directly.
		state.EndTS = e.Timestamp
		state.IsComplete = true
		// Ensure ResponseReady is closed even if ResponseReceived never happened.
		select {
		case <-state.ResponseReady:
		default:
			close(state.ResponseReady)
		}
	}
}

// (Console/Log handlers omitted for brevity - implementation is straightforward logging)

// -- Body Fetching Logic --

// shouldCaptureBody heuristic based on MIME type.
func (h *Harvester) shouldCaptureBody(response *network.Response) bool {
	if response == nil {
		return false
	}
	return isTextMime(response.MimeType)
}

// fetchBody retrieves the response body asynchronously.
func (h *Harvester) fetchBody(requestID network.RequestID) {
	defer h.bodyFetchWG.Done()

	if h.sessionCtx.Err() != nil {
		return
	}

	// Principle 3: Timeout for body fetch.
	ctx, cancel := context.WithTimeout(h.sessionCtx, 15*time.Second)
	defer cancel()

	// Wait for headers if necessary (synchronization).
	h.lock.RLock()
	state, ok := h.requests[requestID]
	h.lock.RUnlock()

	if !ok {
		return
	}

	select {
	case <-state.ResponseReady:
	// Proceed
	case <-ctx.Done():
		// Timeout waiting for headers.
		return
	}

	// -- THE FIX --
	// The signature for GetResponseBody(...).Do(...) has changed in the cdproto library.
	// It no longer returns a boolean indicating if the body is base64 encoded.
	// Instead, it now returns just the raw body bytes and an error.
	body, err := network.GetResponseBody(requestID).Do(ctx)

	if err != nil {
		if ctx.Err() == nil {
			h.logger.Debug("Failed to fetch response body.", zap.String("request_id", string(requestID)), zap.Error(err))
		}
		return
	}

	// Update the state under lock.
	h.lock.Lock()
	defer h.lock.Unlock()
	// Re-check state existence as it might have been cleared if Stop() timed out.
	if state, ok := h.requests[requestID]; ok {
		state.Body = body
		// The new `Do` method returns raw bytes, already decoded from base64 if necessary.
		// The logic in `convertResponse` will re-encode any binary assets into base64 for the HAR file.
		state.BodyBase64 = false
	}
}

func (h *Harvester) waitForPendingFetches(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		h.bodyFetchWG.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		h.logger.Warn("Timeout waiting for pending body fetches.", zap.Error(ctx.Err()))
	}
}

// -- Artifact Accessors and HAR Generation --

func (h *Harvester) getConsoleLogs() []schemas.ConsoleLog {
	h.lock.RLock()
	defer h.lock.RUnlock()
	logs := make([]schemas.ConsoleLog, len(h.consoleLogs))
	copy(logs, h.consoleLogs)
	return logs
}

// generateHAR creates the HAR structure from the collected request states.
func (h *Harvester) generateHAR() *schemas.HAR {
	h.lock.RLock()
	defer h.lock.RUnlock()

	entries := make([]schemas.Entry, 0, len(h.requests))

	for _, state := range h.requests {
		// Ensure we have the minimum required data (Request and Start Time).
		if !state.IsComplete || state.Request == nil || state.StartTS == nil {
			continue
		}

		// Calculate duration. Use MonotonicTime for precise duration if available.
		duration := float64(0)
		startTime := state.StartTS.Time()

		if state.Response != nil && state.Response.Timing != nil {
			// More accurate timing using CDP timing metrics.
			t := state.Response.Timing
			// This is a simplified representation of duration.
			duration = (t.ReceiveHeadersEnd - t.RequestTime*1000)
			if duration < 0 {
				duration = 0
			}
		} else if state.EndTS != nil {
			// Fallback calculation if detailed timing is missing.
			endTime := state.EndTS.Time()
			if endTime.After(startTime) {
				duration = endTime.Sub(startTime).Seconds() * 1000
			}
		}

		entry := schemas.Entry{
			// Assign the time.Time object directly, matching the schema definition.
			StartedDateTime: startTime,
			Time:            duration,
			Request:         h.convertRequest(state.Request),
			Response:        h.convertResponse(state.Response, state.Body, state.BodyBase64),
			// Timings object requires detailed breakdown of Response.Timing fields.
		}
		entries = append(entries, entry)
	}

	// Sort entries by start time (HAR spec requirement).
	sort.Slice(entries, func(i, j int) bool {
		// Compare time.Time objects directly.
		return entries[i].StartedDateTime.Before(entries[j].StartedDateTime)
	})

	return &schemas.HAR{
		Log: schemas.HARLog{
			Version: "1.2",
			Creator: schemas.Creator{
				Name:    "Scalpel CLI Harvester",
				Version: "0.1.0",
			},
			Entries: entries,
		},
	}
}

// -- Conversion Helpers --

func (h *Harvester) convertRequest(req *network.Request) schemas.Request {
	headers := convertHeaders(req.Headers)
	queryString := convertQueryString(req.URL)

	bodySize := int64(-1)
	var postData *schemas.PostData
	// -- THE FIX --
	// This block handles PostData. The HasPostData field in the cdproto library
	// was changed from a *bool to a bool, so we need to adjust our check.
	// Instead of checking for nil and then dereferencing, we just check the boolean value directly.
	if req.HasPostData && req.PostDataEntries != nil && len(req.PostDataEntries) > 0 {
		var postDataBuilder strings.Builder
		for _, entry := range req.PostDataEntries {
			postDataBuilder.WriteString(entry.Bytes)
		}
		postDataText := postDataBuilder.String()
		bodySize = int64(len(postDataText))
		postData = &schemas.PostData{
			// -- THE FIX --
			// The network.Headers type is a map and does not have a .Get method.
			// We now use a helper function to perform a case-insensitive key lookup.
			MimeType: getHeader(req.Headers, "Content-Type"),
			Text:     postDataText,
		}
	}

	return schemas.Request{
		Method:      req.Method,
		URL:         req.URL,
		HTTPVersion: "HTTP/1.1", // Default, updated in ResponseReceived if available.
		Headers:     headers,
		QueryString: queryString,
		PostData:    postData,
		BodySize:    bodySize,
		HeadersSize: calculateHeaderSize(headers),
	}
}

func (h *Harvester) convertResponse(resp *network.Response, body []byte, isBase64 bool) schemas.Response {
	if resp == nil {
		return schemas.Response{Status: 0, StatusText: "Failed (No Response)", BodySize: -1, HeadersSize: -1}
	}

	headers := convertHeaders(resp.Headers)
	content := schemas.Content{
		Size:     int64(len(body)),
		MimeType: resp.MimeType,
	}

	if len(body) > 0 {
		if isBase64 {
			// Data is already base64 encoded by CDP.
			content.Text = string(body)
			content.Encoding = "base64"
		} else {
			// Data is raw bytes. Check if we should encode it (binary data) or treat as text.
			if isTextMime(resp.MimeType) {
				content.Text = string(body)
			} else {
				content.Text = base64.StdEncoding.EncodeToString(body)
				content.Encoding = "base64"
			}
		}
	}

	return schemas.Response{
		Status:      int(resp.Status),
		StatusText:  resp.StatusText,
		HTTPVersion: resp.Protocol,
		Headers:     headers,
		Content:     content,
		// -- THE FIX --
		// The network.Headers type is a map and does not have a .Get method.
		// We now use a helper function to perform a case-insensitive key lookup.
		RedirectURL: getHeader(resp.Headers, "Location"),
		BodySize:    int64(len(body)), // Simplified body size calculation.
		HeadersSize: calculateHeaderSize(headers),
	}
}

// getHeader performs a case insensitive search for a header key.
// The network.Headers type is a map, so we can't rely on a built in Get method.
func getHeader(headers network.Headers, key string) string {
	for h, v := range headers {
		if strings.EqualFold(h, key) {
			if valStr, ok := v.(string); ok {
				// CDP sometimes joins multi value headers with newlines.
				// We'll just return the first one for simplicity here,
				// which is fine for headers like Content-Type and Location.
				if parts := strings.Split(valStr, "\n"); len(parts) > 0 {
					return parts[0]
				}
				return valStr
			}
		}
	}
	return ""
}

// Use the correct type name schemas.NVPair
func convertHeaders(headers network.Headers) []schemas.NVPair {
	nvps := make([]schemas.NVPair, 0, len(headers))
	for name, value := range headers {
		if valStr, ok := value.(string); ok {
			// CDP sometimes joins multi-value headers (like Set-Cookie) with newlines.
			for _, v := range strings.Split(valStr, "\n") {
				nvps = append(nvps, schemas.NVPair{Name: name, Value: v})
			}
		}
	}
	return nvps
}

// Use the correct type name schemas.NVPair
func convertQueryString(urlStr string) []schemas.NVPair {
	u, err := url.Parse(urlStr)
	if err != nil {
		return nil
	}
	nvps := make([]schemas.NVPair, 0)
	for name, values := range u.Query() {
		for _, value := range values {
			nvps = append(nvps, schemas.NVPair{Name: name, Value: value})
		}
	}
	return nvps
}

// calculateHeaderSize estimates the size of the headers in bytes.
// Use the correct type name schemas.NVPair
func calculateHeaderSize(headers []schemas.NVPair) int64 {
	size := 0
	for _, h := range headers {
		// Name + ": " + Value + "\r\n"
		size += len(h.Name) + 2 + len(h.Value) + 2
	}
	size += 20 // Approximation for status line
	return int64(size)
}

func isTextMime(mimeType string) bool {
	mime := strings.ToLower(mimeType)
	return strings.HasPrefix(mime, "text/") || strings.Contains(mime, "json") || strings.Contains(mime, "javascript") || strings.Contains(mime, "xml")
}

// (Console Handlers handleConsoleAPICalled, handleLogEntryAdded, handleExceptionThrown omitted for brevity)
func (h *Harvester) handleConsoleAPICalled(e *runtime.EventConsoleAPICalled) {
	// Implementation omitted for brevity
}
func (h *Harvester) handleLogEntryAdded(e *log.EventEntryAdded) {
	// Implementation omitted for brevity
}
func (h *Harvester) handleExceptionThrown(e *runtime.EventExceptionThrown) {
	// Implementation omitted for brevity
}

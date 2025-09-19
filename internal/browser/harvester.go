package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
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

// valueOnlyContext is a context that is not cancellable and carries only values.
type valueOnlyContext struct{ context.Context }

func (valueOnlyContext) Deadline() (deadline time.Time, ok bool) { return }
func (valueOnlyContext) Done() <-chan struct{}                   { return nil }
func (valueOnlyContext) Err() error                              { return nil }

// requestState keeps tabs on the lifecycle of a single network request.
type requestState struct {
	Request       *network.Request
	Response      *network.Response
	StartTS       *cdp.TimeSinceEpoch // Wall time for HAR StartedDateTime
	StartMonoTS   *cdp.MonotonicTime  // Monotonic time for accurate duration calculation
	EndTS         *cdp.MonotonicTime
	ResponseReady chan struct{} // Signals when response headers are received
	Body          []byte
	IsComplete    bool
}

// Harvester is the workhorse that listens to browser events. It collects network
// traffic, console logs, and exceptions to build a comprehensive picture of what
// the page is doing.
type Harvester struct {
	logger        *zap.Logger
	captureBodies bool

	// The context for the browser tab this harvester is attached to.
	sessionCtx context.Context
	// A separate context for the listener goroutine so it can be stopped cleanly.
	listenerCtx    context.Context
	cancelListener context.CancelFunc

	// -- Data storage and synchronization --
	requests         map[network.RequestID]*requestState
	inflightRequests map[network.RequestID]bool // Specifically for WaitNetworkIdle tracking
	consoleLogs      []schemas.ConsoleLog
	lock             sync.RWMutex

	// Tracks active body fetching goroutines to ensure we don't shut down prematurely.
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

// Start kicks off the event listening process.
func (h *Harvester) Start() error {
	h.lock.Lock()
	defer h.lock.Unlock()

	if h.isStarted {
		return nil
	}

	// This context is derived from the session, so if the session dies, the listener dies.
	h.listenerCtx, h.cancelListener = context.WithCancel(h.sessionCtx)

	// Spin up the listener in the background.
	go h.listen()

	// Tell Chrome what we're interested in.
	err := chromedp.Run(h.sessionCtx,
		network.Enable(),
		runtime.Enable(),
		log.Enable(),
	)

	if err != nil {
		// If the session context is done, this error is expected and can be ignored.
		if h.sessionCtx.Err() != nil {
			return nil
		}
		h.cancelListener() // Clean up if we fail to enable the domains.
		return err
	}

	h.isStarted = true
	h.logger.Debug("Harvester started and listening for events.")
	return nil
}

// listen is the main event loop that receives and dispatches CDP events.
func (h *Harvester) listen() {
	chromedp.ListenTarget(h.listenerCtx, func(ev interface{}) {
		switch e := ev.(type) {
		// -- Network Events --
		case *network.EventRequestWillBeSent:
			h.handleRequestWillBeSent(e)
		case *network.EventResponseReceived:
			h.handleResponseReceived(e)
		case *network.EventLoadingFinished:
			h.handleLoadingFinished(e)
		case *network.EventLoadingFailed:
			h.handleLoadingFailed(e)

		// -- Console and Runtime Events --
		case *runtime.EventConsoleAPICalled:
			h.handleConsoleAPICalled(e)
		case *log.EventEntryAdded:
			h.handleLogEntryAdded(e)
		case *runtime.EventExceptionThrown:
			h.handleExceptionThrown(e)
		}
	})
}

// Stop halts the collection of events, waits for any in flight operations to
// finish, and returns the collected artifacts.
func (h *Harvester) Stop(ctx context.Context) (*schemas.HAR, []schemas.ConsoleLog) {
	h.lock.Lock()
	if !h.isStarted {
		h.lock.Unlock()
		return h.generateHAR(), h.getConsoleLogs()
	}

	// Tell the listener goroutine to pack it up.
	if h.cancelListener != nil {
		h.cancelListener()
		h.cancelListener = nil
	}
	h.isStarted = false
	h.lock.Unlock()

	h.logger.Debug("Harvester stopped. Waiting for pending body fetches to complete.")

	// This is a crucial step. We wait here to make sure all asynchronous body
	// fetches have either completed or timed out before we generate the HAR.
	h.waitForPendingFetches(ctx)

	// Now we can safely build the final artifacts.
	return h.generateHAR(), h.getConsoleLogs()
}

// WaitNetworkIdle is a dynamic wait that polls until there are no in flight
// network requests for a specified duration.
func (h *Harvester) WaitNetworkIdle(ctx context.Context, quietPeriod time.Duration) error {
	// A ticker is a clean way to poll at regular intervals.
	ticker := time.NewTicker(quietPeriod / 2) // Check more frequently than the quiet period.
	defer ticker.Stop()

	lastActivity := time.Now()
	for {
		select {
		case <-ctx.Done():
			h.logger.Debug("WaitNetworkIdle aborted due to context cancellation.", zap.Error(ctx.Err()))
			return ctx.Err()
		case <-ticker.C:
			h.lock.RLock()
			inflightCount := len(h.inflightRequests)
			h.lock.RUnlock()

			if inflightCount > 0 {
				lastActivity = time.Now() // Reset the timer if there's activity.
				h.logger.Debug("Waiting for network idle...", zap.Int("inflight_requests", inflightCount))
			} else if time.Since(lastActivity) >= quietPeriod {
				// We've had no activity for the entire quiet period. We're idle.
				return nil
			}
		}
	}
}

// -- Event Handlers --

func (h *Harvester) handleRequestWillBeSent(e *network.EventRequestWillBeSent) {
	h.lock.Lock()
	defer h.lock.Unlock()

	h.inflightRequests[e.RequestID] = true

	// If this is a redirect, the previous request under this ID is now complete.
	if e.RedirectResponse != nil {
		if prevState, ok := h.requests[e.RequestID]; ok && !prevState.IsComplete {
			prevState.Response = e.RedirectResponse
			prevState.IsComplete = true
			// Use the timestamp of the redirect event as the end time for the previous leg.
			prevState.EndTS = e.Timestamp
			// Unblock any potential body fetcher for the redirected request.
			select {
			case <-prevState.ResponseReady:
			default:
				close(prevState.ResponseReady)
			}
		}
	}

	// A new request (or the next leg of a redirect) is starting.
	h.requests[e.RequestID] = &requestState{
		Request:       e.Request,
		StartTS:       e.WallTime,
		StartMonoTS:   e.Timestamp, // Capture the monotonic start time.
		ResponseReady: make(chan struct{}),
	}
}

func (h *Harvester) handleResponseReceived(e *network.EventResponseReceived) {
	h.lock.Lock()
	defer h.lock.Unlock()

	if state, ok := h.requests[e.RequestID]; ok {
		state.Response = e.Response
		// Signal that the headers are here, unblocking any pending body fetch.
		close(state.ResponseReady)
	}
}

func (h *Harvester) handleLoadingFinished(e *network.EventLoadingFinished) {
	h.lock.Lock()

	delete(h.inflightRequests, e.RequestID)

	state, ok := h.requests[e.RequestID]
	if !ok {
		h.lock.Unlock()
		return
	}

	state.EndTS = e.Timestamp
	state.IsComplete = true

	if h.captureBodies && h.shouldCaptureBody(state.Response) {
		h.bodyFetchWG.Add(1)
		// Important to unlock before the goroutine to avoid potential deadlocks.
		h.lock.Unlock()
		go h.fetchBody(e.RequestID)
	} else {
		h.lock.Unlock()
	}
}

func (h *Harvester) handleLoadingFailed(e *network.EventLoadingFailed) {
	h.lock.Lock()
	defer h.lock.Unlock()

	delete(h.inflightRequests, e.RequestID)

	if state, ok := h.requests[e.RequestID]; ok {
		state.EndTS = e.Timestamp
		state.IsComplete = true
		// Make sure to unblock any waiting fetcher even on failure.
		select {
		case <-state.ResponseReady:
		default:
			close(state.ResponseReady)
		}
	}
}

// -- Console and Log Handlers --

func (h *Harvester) handleConsoleAPICalled(e *runtime.EventConsoleAPICalled) {
	var textBuilder strings.Builder
	for i, arg := range e.Args {
		if i > 0 {
			textBuilder.WriteString(" ")
		}
		// Go through hoops to get a clean string representation of the console argument.
		var val interface{}
		if arg.Value != nil && json.Unmarshal(arg.Value, &val) == nil {
			textBuilder.WriteString(fmt.Sprintf("%v", val))
		} else if arg.Description != "" {
			textBuilder.WriteString(arg.Description)
		} else {
			textBuilder.WriteString(fmt.Sprintf("[%s]", arg.Type))
		}
	}

	logEntry := schemas.ConsoleLog{
		Timestamp: e.Timestamp.Time(),
		Type:      string(e.Type),
		Text:      textBuilder.String(),
		Source:    "console-api",
	}

	h.lock.Lock()
	defer h.lock.Unlock()
	h.consoleLogs = append(h.consoleLogs, logEntry)
}

func (h *Harvester) handleLogEntryAdded(e *log.EventEntryAdded) {
	if e.Entry == nil {
		return
	}
	logEntry := schemas.ConsoleLog{
		Type:      string(e.Entry.Level),
		Text:      e.Entry.Text,
		Timestamp: e.Entry.Timestamp.Time(),
		Source:    string(e.Entry.Source),
	}

	h.lock.Lock()
	defer h.lock.Unlock()
	h.consoleLogs = append(h.consoleLogs, logEntry)
}

func (h *Harvester) handleExceptionThrown(e *runtime.EventExceptionThrown) {
	if e.ExceptionDetails == nil {
		return
	}
	// The description usually has the most useful info, including the stack trace.
	text := e.ExceptionDetails.Text
	if e.ExceptionDetails.Exception != nil && e.ExceptionDetails.Exception.Description != "" {
		text = e.ExceptionDetails.Exception.Description
	}

	logEntry := schemas.ConsoleLog{
		Type:      "exception",
		Text:      text,
		Timestamp: e.Timestamp.Time(),
		Source:    "runtime",
	}

	h.lock.Lock()
	defer h.lock.Unlock()
	h.consoleLogs = append(h.consoleLogs, logEntry)
}

// -- Body Fetching Logic --

// A simple heuristic to decide if we should bother capturing a response body.
func (h *Harvester) shouldCaptureBody(response *network.Response) bool {
	if response == nil {
		return false
	}
	return isTextMime(response.MimeType)
}

// fetchBody grabs the response body for a given request. Runs in its own goroutine.
func (h *Harvester) fetchBody(requestID network.RequestID) {
	defer h.bodyFetchWG.Done()

	// CRITICAL CORRECTION: Use a detached context (valueOnlyContext) for fetching the body.
	// This inherits the CDP target info but not the cancellation signal from h.sessionCtx.
	// Apply a timeout to the detached context.
	ctx, cancel := context.WithTimeout(valueOnlyContext{h.sessionCtx}, 15*time.Second)
	defer cancel()

	h.lock.RLock()
	state, ok := h.requests[requestID]
	h.lock.RUnlock()

	if !ok {
		return
	}

	// Wait until the response headers have arrived.
	select {
	case <-state.ResponseReady:
		// Headers are here, we're good to go.
	case <-ctx.Done():
		// Timed out waiting for headers.
		h.logger.Debug("Timed out waiting for response headers before fetching body.", zap.String("request_id", string(requestID)))
		return
	}

	body, err := network.GetResponseBody(requestID).Do(ctx)
	if err != nil {
		// Handle expected errors gracefully and reduce log noise.

		// Check if the original session context is done. This indicates the session was closed.
		// Chromedp often returns "invalid context" or "session closed" in this scenario.
		if h.sessionCtx.Err() != nil {
			// Suppress logging this as it's expected during normal shutdown/cleanup.
			return
		}

		if ctx.Err() != nil {
			// Timeout occurred during the fetch itself.
			h.logger.Debug("Response body fetch timed out.", zap.String("request_id", string(requestID)), zap.Error(err))
			return
		}

		// Log other unexpected errors.
		h.logger.Warn("Failed to fetch response body (unexpected error).", zap.String("request_id", string(requestID)), zap.Error(err))
		return
	}

	h.lock.Lock()
	defer h.lock.Unlock()
	// Re check that the state still exists.
	if state, ok := h.requests[requestID]; ok {
		state.Body = body
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
		// All fetches completed.
	case <-ctx.Done():
		h.logger.Warn("Timed out waiting for all response bodies to be fetched.", zap.Error(ctx.Err()))
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

// generateHAR pieces together all the collected request data into the HAR format.
func (h *Harvester) generateHAR() *schemas.HAR {
	h.lock.RLock()
	defer h.lock.RUnlock()

	entries := make([]schemas.Entry, 0, len(h.requests))
	for _, state := range h.requests {
		if !state.IsComplete || state.Request == nil || state.StartTS == nil {
			continue // Skip incomplete entries.
		}

		startTime := state.StartTS.Time()
		duration := float64(0)

		// Calculate duration using monotonic timestamps for accuracy.
		if state.StartMonoTS != nil && state.EndTS != nil {
			// HAR time is in milliseconds.
			duration = state.EndTS.Time().Sub(state.StartMonoTS.Time()).Seconds() * 1000
			if duration < 0 {
				duration = 0 // Defensive check.
			}
		}

		entry := schemas.Entry{
			StartedDateTime: startTime,
			Time:            duration,
			Request:         h.convertRequest(state.Request),
			Response:        h.convertResponse(state.Response, state.Body),
		}
		entries = append(entries, entry)
	}

	// The HAR spec requires entries to be sorted by start time.
	sort.Slice(entries, func(i, j int) bool {
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

	var postData *schemas.PostData
	bodySize := int64(-1)

	// Handle PostDataEntries when available.
	if req.HasPostData && req.PostDataEntries != nil && len(req.PostDataEntries) > 0 {
		// PostDataEntries is an array of data chunks. We must concatenate them.
		var postDataTextBuilder strings.Builder
		for _, entry := range req.PostDataEntries {
			// entry.Bytes is a string, as confirmed by the compiler.
			if entry.Bytes != "" {
				postDataTextBuilder.WriteString(entry.Bytes)
			}
		}

		postDataText := postDataTextBuilder.String()
		if postDataText != "" {
			bodySize = int64(len(postDataText))
			postData = &schemas.PostData{
				MimeType: getHeader(req.Headers, "Content-Type"),
				Text:     postDataText,
				// Params field is omitted unless MIME type is application/x-www-form-urlencoded and parsed.
			}
		}
	}

	return schemas.Request{
		Method:      req.Method,
		URL:         req.URL,
		HTTPVersion: "HTTP/1.1", // Default assumption.
		Headers:     headers,
		QueryString: queryString,
		PostData:    postData,
		BodySize:    bodySize,
		HeadersSize: calculateHeaderSize(headers),
	}
}

func (h *Harvester) convertResponse(resp *network.Response, body []byte) schemas.Response {
	if resp == nil {
		return schemas.Response{Status: 0, StatusText: "Failed (No Response)", BodySize: -1, HeadersSize: -1}
	}

	headers := convertHeaders(resp.Headers)
	content := schemas.Content{
		Size:     int64(len(body)),
		MimeType: resp.MimeType,
	}

	if len(body) > 0 {
		// If it's text, keep it as is. If it's binary, encode it to base64 for the HAR.
		if isTextMime(resp.MimeType) {
			content.Text = string(body)
		} else {
			content.Text = base64.StdEncoding.EncodeToString(body)
			content.Encoding = "base64"
		}
	}

	// Calculate header size for accurate BodySize calculation.
	headersSize := calculateHeaderSize(headers)

	// HAR BodySize: Size of the encoded response body.
	// EncodedDataLength is often the total bytes received (headers + body).
	calculatedBodySize := int64(resp.EncodedDataLength) - headersSize
	if calculatedBodySize < 0 {
		// Fallback if EncodedDataLength is unreliable or doesn't include headers (e.g., redirects, cached responses).
		// In this case, we use the EncodedDataLength as the BodySize, or the content size if EncodedDataLength is zero.
		if resp.EncodedDataLength > 0 {
			calculatedBodySize = int64(resp.EncodedDataLength)
		} else {
			calculatedBodySize = content.Size
		}
	}

	return schemas.Response{
		Status:      int(resp.Status),
		StatusText:  resp.StatusText,
		HTTPVersion: resp.Protocol,
		Headers:     headers,
		Content:     content,
		RedirectURL: getHeader(resp.Headers, "Location"),
		BodySize:    calculatedBodySize,
		HeadersSize: headersSize,
	}
}

// getHeader performs a case insensitive search for a header key.
func getHeader(headers network.Headers, key string) string {
	for h, v := range headers {
		if strings.EqualFold(h, key) {
			if valStr, ok := v.(string); ok {
				// CDP can join multi value headers with newlines, just take the first.
				return strings.Split(valStr, "\n")[0]
			}
		}
	}
	return ""
}

// convertHeaders converts CDP headers to HAR NVPairs, ensuring deterministic order.
func convertHeaders(headers network.Headers) []schemas.NVPair {
	nvps := make([]schemas.NVPair, 0, len(headers))

	// Sort keys for deterministic header order (important for accurate size calculation and testing).
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, name := range keys {
		value := headers[name]
		if valStr, ok := value.(string); ok {
			// Handle multi value headers like Set-Cookie.
			for _, v := range strings.Split(valStr, "\n") {
				nvps = append(nvps, schemas.NVPair{Name: name, Value: v})
			}
		}
	}
	return nvps
}

// convertQueryString converts the URL query parameters to HAR NVPairs, ensuring deterministic order.
func convertQueryString(urlStr string) []schemas.NVPair {
	u, err := url.Parse(urlStr)
	if err != nil {
		return nil
	}
	nvps := make([]schemas.NVPair, 0)
	query := u.Query()

	// Sort keys for deterministic order.
	keys := make([]string, 0, len(query))
	for k := range query {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, name := range keys {
		values := query[name]
		for _, value := range values {
			nvps = append(nvps, schemas.NVPair{Name: name, Value: value})
		}
	}
	return nvps
}

// A rough estimation of header size based on HTTP/1.1 format.
func calculateHeaderSize(headers []schemas.NVPair) int64 {
	size := 0
	// Estimate the status line (e.g., "HTTP/1.1 200 OK\r\n") - ~20 bytes.
	// This is a heuristic, as the actual size depends on the protocol version and status code/text.
	size += 20
	for _, h := range headers {
		// Name + ": " + Value + "\r\n"
		size += len(h.Name) + 2 + len(h.Value) + 2
	}
	// Final blank line "\r\n"
	size += 2
	return int64(size)
}

func isTextMime(mimeType string) bool {
	mime := strings.ToLower(mimeType)
	return strings.HasPrefix(mime, "text/") ||
		strings.Contains(mime, "json") ||
		strings.Contains(mime, "javascript") ||
		strings.Contains(mime, "xml") ||
		strings.Contains(mime, "x-www-form-urlencoded")
}

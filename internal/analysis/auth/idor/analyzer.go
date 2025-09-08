// internal/analysis/auth/idor/analyzer.go
package idor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/xkilldash9x/scalpel-cli/internal/analysis/core"
)

// The Analyzer is the big cheese, orchestrating the whole IDOR testing show.
// It manages user sessions, runs different test strategies, and reports its findings.
type Analyzer struct {
	ScanID      uuid.UUID
	logger      *zap.Logger
	sessions    map[string]*SessionContext
	reporter    core.Reporter
	mu          sync.RWMutex // Protects the sessions map from concurrent mayhem.
	concurrency int
}

// Creates a new IDOR analyzer, ready for action.
// The concurrency level dictates how many test requests we can fire off in parallel.
func NewAnalyzer(scanID uuid.UUID, logger *zap.Logger, reporter core.Reporter, concurrency int) *Analyzer {
	if concurrency <= 0 {
		concurrency = 10 // A sensible default.
	}
	return &Analyzer{
		ScanID:      scanID,
		logger:      logger.Named("idor_analyzer"),
		sessions:    make(map[string]*SessionContext),
		reporter:    reporter,
		concurrency: concurrency,
	}
}

// Sets up a new user persona for our testing shenanigans, complete with its own isolated cookie jar.
func (a *Analyzer) InitializeSession(role string) error {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return fmt.Errorf("failed to create cookie jar for role %s: %w", role, err)
	}
	client := &http.Client{
		Jar:     jar,
		Timeout: 15 * time.Second,
		// This is key. We disable redirects to see the actual authorization responses (like a 302 vs. a 403)
		// instead of blindly following them to a login page.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Lock it down before we mess with the sessions map.
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, exists := a.sessions[role]; exists {
		return fmt.Errorf("session role %s already initialized", role)
	}

	a.sessions[role] = &SessionContext{
		Role:             role,
		Client:           client,
		ObservedRequests: make(map[string]ObservedRequest),
	}
	return nil
}

// This gets called during the crawl. It runs requests for a given role and squirrels away the interesting ones for later analysis.
func (a *Analyzer) ObserveAndExecute(ctx context.Context, role string, req *http.Request, body []byte) (*http.Response, error) {
	// Safely grab the session without causing a scene.
	a.mu.RLock()
	session, exists := a.sessions[role]
	a.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("session role %s not initialized", role)
	}

	// Get the request body ready for its journey.
	if len(body) > 0 {
		req.Body = io.NopCloser(bytes.NewReader(body))
	}

	// And... we're off! Execute the request.
	resp, err := session.Client.Do(req.WithContext(ctx))
	if err != nil {
		return resp, err
	}
	defer resp.Body.Close()

	// We need to read the whole response body to analyze it, but also need to hand it back to the caller.
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		a.logger.Warn("Failed to read response body during observation, skipping", zap.Error(err), zap.String("url", req.URL.String()))
		// We still return the response, just with a fresh body stream.
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		return resp, nil
	}
	resp.Body = io.NopCloser(bytes.NewReader(respBody)) // Reset for the caller.

	// Now, let's see if this request has anything juicy in it.
	identifiers := ExtractIdentifiers(req, body)

	if len(identifiers) > 0 {
		requestKey := fmt.Sprintf("%s %s", req.Method, req.URL.Path)

		// Clone the request now to keep the lock time short and sweet.
		clonedReq := req.Clone(ctx)

		// Time to save our findings. Lock the session to prevent concurrent writes.
		session.mu.Lock()
		defer session.mu.Unlock()

		session.ObservedRequests[requestKey] = ObservedRequest{
			Request:        clonedReq,
			Body:           body,
			Identifiers:    identifiers,
			BaselineStatus: resp.StatusCode,
			BaselineLength: int64(len(respBody)),
		}
	}

	return resp, nil
}

// Kicks off the main analysis phase. This is where the magic happens, concurrently of course.
func (a *Analyzer) RunAnalysis(ctx context.Context, primaryRole, secondaryRole string) error {
	// Quick check to make sure our players are on the field.
	a.mu.RLock()
	primarySession, okP := a.sessions[primaryRole]
	secondarySession, okS := a.sessions[secondaryRole]
	a.mu.RUnlock()

	if !okP || !okS {
		return fmt.Errorf("both primary (%s) and secondary (%s) roles must be initialized", primaryRole, secondaryRole)
	}

	primarySession.mu.RLock()
	observedCount := len(primarySession.ObservedRequests)
	primarySession.mu.RUnlock()

	a.logger.Info("Starting IDOR analysis",
		zap.String("Victim", primaryRole),
		zap.String("Attacker", secondaryRole),
		zap.Int("ObservedRequests", observedCount))

	var wg sync.WaitGroup

	// Strategy 1: Horizontal IDOR. Let's see if the attacker can access the victim's stuff.
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.testHorizontal(ctx, primarySession, secondarySession)
	}()

	// Strategy 2: Predictive IDOR. Can we guess our way into other resources?
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.testPredictive(ctx, primarySession)
	}()

	wg.Wait()
	a.logger.Info("IDOR analysis completed")

	return nil
}

// The horizontal test strategy. We're replaying the victim's requests with the attacker's session to see if we can sneak in.
func (a *Analyzer) testHorizontal(ctx context.Context, primarySession *SessionContext, secondarySession *SessionContext) {
	jobs := make(chan ObservedRequest, len(primarySession.ObservedRequests))

	// Load up the job queue.
	primarySession.mu.RLock()
	for _, observed := range primarySession.ObservedRequests {
		jobs <- observed
	}
	primarySession.mu.RUnlock()
	close(jobs)

	var wg sync.WaitGroup

	// Spin up the worker pool.
	for i := 0; i < a.concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.horizontalWorker(ctx, primarySession, secondarySession, jobs)
		}()
	}

	wg.Wait()
}

// This is the workhorse for the horizontal tests, pulling jobs from the channel and doing the actual replay.
func (a *Analyzer) horizontalWorker(ctx context.Context, primarySession *SessionContext, secondarySession *SessionContext, jobs <-chan ObservedRequest) {
	for observed := range jobs {
		select {
		case <-ctx.Done():
			return // The boss said quit, so we quit.
		default:
		}

		// Prep a clone of the request for replay.
		req := observed.Request.Clone(ctx)
		// CRITICAL FIX: The original request, after being executed by the victim's client, will have the victim's
		// 'Cookie' header attached. Cloning preserves this header. We MUST remove it to force the attacker's client
		// to use its own cookie jar, which is the entire point of the horizontal test.
		req.Header.Del("Cookie")

		var reqBody []byte
		if len(observed.Body) > 0 {
			reqBody = observed.Body
			req.Body = io.NopCloser(bytes.NewReader(observed.Body))
		}

		// Replay the request, but this time as the attacker.
		resp, err := secondarySession.Client.Do(req)
		if err != nil {
			a.logger.Debug("Network error during IDOR replay", zap.Error(err), zap.String("url", req.URL.String()))
			continue
		}
		defer resp.Body.Close() // Clean up after ourselves.

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			a.logger.Warn("Failed to read response body during IDOR analysis", zap.Error(err), zap.String("url", req.URL.String()))
			continue
		}

		currentLength := int64(len(respBody))

		// The moment of truth. Does the response look the same as the victim's?
		if resp.StatusCode == observed.BaselineStatus {
			lengthDiff := abs(currentLength - observed.BaselineLength)
			// Allow a 10% tolerance for small dynamic content changes.
			tolerance := int64(float64(observed.BaselineLength) * 0.1)

			if lengthDiff <= tolerance {
				// Bingo. We've got a potential vulnerability.
				key := fmt.Sprintf("%s %s", req.Method, req.URL.Path)
				description := fmt.Sprintf("A resource belonging to %s (Victim) was successfully accessed by %s (Attacker). The status code (%d) and content length (%d vs baseline %d) match the original request, indicating an access control failure (BOLA).",
					primarySession.Role, secondarySession.Role, resp.StatusCode, currentLength, observed.BaselineLength)

				evidence := &core.Evidence{
					Summary: fmt.Sprintf("Horizontal access to %s", key),
					Request: &core.SerializedRequest{
						Method:  req.Method,
						URL:     req.URL.String(),
						Headers: req.Header,
						Body:    string(reqBody),
					},
					Response: &core.SerializedResponse{
						StatusCode: resp.StatusCode,
						Headers:    resp.Header,
						Body:       string(respBody),
					},
				}

				a.reportFinding(
					"Horizontal Insecure Direct Object Reference (IDOR)",
					description,
					core.SeverityHigh,
					req.URL.String(),
					evidence,
					"CWE-284", // Improper Access Control
				)
			}
		}
	}
}

// The predictive test strategy. Can we just guess the next ID in the sequence? Let's find out.
func (a *Analyzer) testPredictive(ctx context.Context, session *SessionContext) {
	// Prep the job list with every identifier we found.
	session.mu.RLock()
	var jobList []PredictiveJob
	for _, observed := range session.ObservedRequests {
		for _, identifier := range observed.Identifiers {
			jobList = append(jobList, PredictiveJob{Observed: observed, Identifier: identifier})
		}
	}
	session.mu.RUnlock()

	jobs := make(chan PredictiveJob, len(jobList))
	for _, job := range jobList {
		jobs <- job
	}
	close(jobs)

	var wg sync.WaitGroup

	// Unleash the workers.
	for i := 0; i < a.concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.predictiveWorker(ctx, session, jobs)
		}()
	}

	wg.Wait()
}

// The workhorse for the predictive tests. It takes a job, messes with the ID, and sees what breaks.
func (a *Analyzer) predictiveWorker(ctx context.Context, session *SessionContext, jobs <-chan PredictiveJob) {
	for job := range jobs {
		select {
		case <-ctx.Done():
			return
		default:
		}

		observed := job.Observed
		identifier := job.Identifier

		// Can we even generate a predictable value for this?
		testValue, err := GenerateTestValue(identifier)
		if err != nil {
			// Nope. On to the next one (looking at you, UUIDs).
			continue
		}

		// Let's build our shiny, modified request.
		testReq, testBody, err := ApplyTestValue(observed.Request, observed.Body, identifier, testValue)
		if err != nil {
			a.logger.Debug("Failed to apply test value", zap.Error(err))
			continue
		}

		if len(testBody) > 0 {
			testReq.Body = io.NopCloser(bytes.NewReader(testBody))
		}

		// Send the modified request using the original user's session.
		resp, err := session.Client.Do(testReq.WithContext(ctx))
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			a.logger.Warn("Failed to read response body during predictive IDOR analysis", zap.Error(err), zap.String("url", testReq.URL.String()))
			continue
		}

		// Did it work? A 2xx status code is a pretty good sign.
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			// Yep, it worked. We've got another one.
			key := fmt.Sprintf("%s %s", observed.Request.Method, observed.Request.URL.Path)
			description := fmt.Sprintf("The application granted access to a resource by predicting an identifier. The original identifier '%s' (Type: %s) at location '%s' (Key: %s) was modified to '%s', and the request succeeded (Status: %d).",
				identifier.Value, identifier.Type, identifier.Location, identifier.Key, testValue, resp.StatusCode)

			evidence := &core.Evidence{
				Summary: fmt.Sprintf("Predictive access to %s using modified ID %s", key, testValue),
				Request: &core.SerializedRequest{
					Method:  testReq.Method,
					URL:     testReq.URL.String(),
					Headers: testReq.Header,
					Body:    string(testBody),
				},
				Response: &core.SerializedResponse{
					StatusCode: resp.StatusCode,
					Headers:    resp.Header,
					Body:       string(respBody),
				},
			}

			a.reportFinding(
				"Predictive Insecure Direct Object Reference (IDOR)",
				description,
				core.SeverityMedium,
				testReq.URL.String(),
				evidence,
				"CWE-639", // Authorization Bypass Through User Controlled Key
			)
		}
	}
}

// Packages up our findings into a neat report and sends it off.
func (a *Analyzer) reportFinding(title, description string, severity core.SeverityLevel, targetURL string, evidence *core.Evidence, cwe string) {
	finding := core.AnalysisResult{
		ScanID:            a.ScanID,
		AnalyzerName:      "IDORAnalyzer",
		Timestamp:         time.Now().UTC(),
		VulnerabilityType: "BrokenObjectLevelAuthorization",
		Title:             title,
		Description:       description,
		Severity:          severity,
		Status:            core.StatusOpen,
		Confidence:        0.95, // High confidence since we actively triggered it.
		TargetURL:         targetURL,
		Evidence:          evidence,
		CWE:               cwe,
	}
	if err := a.reporter.Publish(finding); err != nil {
		a.logger.Error("Failed to publish finding", zap.Error(err), zap.String("title", title))
	}
}

// A quick and dirty absolute value helper for int64.
func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
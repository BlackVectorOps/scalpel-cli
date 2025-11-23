// internal/browser/stealth/js_test.go
package stealth

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/debugger"
	"github.com/chromedp/cdproto/profiler"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xkilldash9x/scalpel-cli/api/schemas"
)

//go:embed evasions.test.js
var EvasionsTestJS string

// TestJavascriptEvasions runs the embedded JavaScript unit tests within a browser context.
func TestJavascriptEvasions(t *testing.T) {
	// 1. Setup Browser Context (using the helper defined in stealth_test.go)
	ctx, cancel := setupBrowserContext(t)
	defer cancel()

	// 2. Define the Persona specifically for the JS tests
	// We use distinct values to ensure the overrides are working.
	testPersona := schemas.Persona{
		UserAgent:     "Mozilla/5.0 (ScalpelJSTest/1.0)",
		Platform:      "JSTestOS",
		Languages:     []string{"js-TEST", "js"},
		Width:         1280,
		Height:        720,
		AvailWidth:    1280,
		AvailHeight:   700, // Slightly less than height
		ColorDepth:    32,  // Used for screen.colorDepth/pixelDepth in JS
		PixelDepth:    2,   // Used for Device Pixel Ratio (DPR) in CDP
		Mobile:        false,
		WebGLVendor:   "Test Vendor",
		WebGLRenderer: "Test Renderer",
	}

	// 3. Apply Evasions (This injects EvasionsJS and the Persona data via EvaluateOnNewDocument)
	// FIX: We must use chromedp.Run() instead of Tasks.Do(). Apply() returns low-level CDP actions
	// which require the context handled by the chromedp runner to access the browser executor.
	// Tasks.Do() bypasses this, causing "invalid context".
	err := chromedp.Run(ctx, Apply(testPersona, nil))
	require.NoError(t, err, "Applying stealth evasions failed")

	// -- Coverage Setup --
	// Enable Profiler and Debugger domains to capture JS execution stats.
	err = chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			// FIX: debugger.Enable() returns (debugger.ID, error). Ignore the ID.
			if _, err := debugger.Enable().Do(ctx); err != nil {
				return err
			}
			// profiler.Enable() usually returns just error, but we handle it cleanly.
			if err := profiler.Enable().Do(ctx); err != nil {
				return err
			}

			// FIX: profiler.StartPreciseCoverage returns (timestamp, error).
			// We cannot return it directly because ActionFunc expects func() error.
			_, err := profiler.StartPreciseCoverage().
				WithDetailed(true).
				WithCallCount(false).
				Do(ctx)
			return err
		}),
	)
	require.NoError(t, err, "Failed to enable CDP profiler/debugger")

	// 4. Setup Test Server
	// The server serves a blank page where we can run the tests.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body><h1>Running JS Evasion Tests...</h1></body></html>`)
	}))
	defer server.Close()

	// 5. Navigate, Inject Test Runner, and Retrieve Results
	var rawResults interface{}
	err = chromedp.Run(ctx,
		chromedp.Navigate(server.URL),
		chromedp.WaitVisible("body"),
		// Inject the test runner script (evasions_test.js)
		chromedp.Evaluate(EvasionsTestJS, nil),
		// Wait for the tests to complete (they run asynchronously in the JS)
		// We use chromedp.Poll to wait robustly for the results variable to be set.
		chromedp.Poll(`window.SCALPEL_TEST_RESULTS`, &rawResults, chromedp.WithPollingTimeout(10*time.Second)),
	)
	require.NoError(t, err, "Chromedp run for JS tests failed or timed out waiting for results")
	require.NotNil(t, rawResults, "JS Test results should not be nil")

	// -- Coverage Collection --
	// Capture the coverage data before we process results.
	calculateJSCoverage(t, ctx)

	// 6. Process Results
	// Convert the raw interface{} result into a structured format for analysis.
	resultsJSON, err := json.Marshal(rawResults)
	require.NoError(t, err, "Failed to marshal raw results")

	// Capture stack trace for better debugging
	type TestResult struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
		Stack  string `json:"stack,omitempty"`
	}
	var results []TestResult
	err = json.Unmarshal(resultsJSON, &results)
	require.NoError(t, err, "Failed to unmarshal test results")

	require.NotEmpty(t, results, "Should have received test results")

	// 7. Report Results
	failed := false
	t.Log("--- JavaScript Evasion Test Results (In-Browser) ---")
	for _, result := range results {
		if result.Status == "FAIL" {
			failed = true
			t.Errorf("FAIL: %s\n    Error: %s\n    Stack (JS):\n%s", result.Name, result.Error, result.Stack)
		} else {
			t.Logf("PASS: %s", result.Name)
		}
	}
	t.Log("----------------------------------------------------")

	assert.False(t, failed, "Some JavaScript evasion tests failed. Check logs above.")
}

// calculateJSCoverage retrieves coverage data from CDP, identifies the evasions script,
// and logs the percentage of code executed.
func calculateJSCoverage(t *testing.T, ctx context.Context) {
	var coverage []*profiler.ScriptCoverage
	err := chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			// FIX: TakePreciseCoverage returns ([]ScriptCoverage, timestamp, error).
			// We ignore the timestamp (2nd return value).
			coverage, _, err = profiler.TakePreciseCoverage().Do(ctx)
			return err
		}),
	)
	if err != nil {
		t.Logf("Warning: Failed to retrieve JS coverage: %v", err)
		return
	}

	found := false
	for _, script := range coverage {
		// We need to identify our injected script. Since it's injected via
		// Page.addScriptToEvaluateOnNewDocument, it might not have a friendly URL.
		// We fetch the source and check for a unique signature ("SCALPEL_PERSONA").
		var source string
		err := chromedp.Run(ctx,
			chromedp.ActionFunc(func(ctx context.Context) error {
				var err error
				// FIX: GetScriptSource returns (source, bytecode, error).
				// We ignore the bytecode (2nd return value).
				source, _, err = debugger.GetScriptSource(script.ScriptID).Do(ctx)
				return err
			}),
		)
		if err != nil {
			continue
		}

		// Check for our signature
		if strings.Contains(source, "SCALPEL_PERSONA") && strings.Contains(source, "evasions.js") {
			found = true
			totalBytes := float64(len(source))
			coveredBytes := calculateCoveredBytes(script.Functions)

			percentage := (coveredBytes / totalBytes) * 100.0
			t.Logf("--- JS Code Coverage: %.2f%% (%d/%d bytes) ---", percentage, int64(coveredBytes), int64(totalBytes))

			// Threshold check (optional, but good for sanity)
			if percentage < 10.0 {
				t.Log("Warning: JS Coverage is suspiciously low. Are the tests actually exercising the evasions?")
			}
			break
		}
	}

	if !found {
		t.Log("Warning: Could not locate 'evasions.js' in CDP coverage data.")
	}
}

// calculateCoveredBytes merges overlapping execution ranges and sums the unique covered bytes.
func calculateCoveredBytes(functions []*profiler.FunctionCoverage) float64 {
	type interval struct {
		start, end int64
	}
	var ranges []interval

	// Collect all ranges where Count > 0 (code was executed)
	for _, fn := range functions {
		for _, r := range fn.Ranges {
			if r.Count > 0 {
				ranges = append(ranges, interval{start: r.StartOffset, end: r.EndOffset})
			}
		}
	}

	if len(ranges) == 0 {
		return 0
	}

	// Sort ranges by start offset
	sort.Slice(ranges, func(i, j int) bool {
		return ranges[i].start < ranges[j].start
	})

	// Merge overlapping intervals
	var merged []interval
	current := ranges[0]

	for i := 1; i < len(ranges); i++ {
		next := ranges[i]
		if next.start < current.end {
			// Overlap: extend current end if next goes further
			if next.end > current.end {
				current.end = next.end
			}
		} else {
			// No overlap: push current and start new
			merged = append(merged, current)
			current = next
		}
	}
	merged = append(merged, current)

	// Sum lengths
	var total int64
	for _, m := range merged {
		total += (m.end - m.start)
	}

	return float64(total)
}

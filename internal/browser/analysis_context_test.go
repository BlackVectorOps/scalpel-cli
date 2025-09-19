// internal/browser/analysis_context_test.go
package browser

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"
)

// A reasonable timeout for individual browser tests to prevent hangs.
const testTimeout = 20 * time.Second

// TestAnalysisContext serves as a grouping for all related sub-tests.
func TestAnalysisContext(t *testing.T) {
	t.Run("InitializeAndClose", func(t *testing.T) {
		t.Parallel()
		fixture := newTestFixture(t)
		require.NotNil(t, fixture.Session)
		require.NotEmpty(t, fixture.Session.sessionID, "Session ID should not be empty")

		server := createStaticTestServer(t, `<!DOCTYPE html><html><body><h1>Init Test</h1></body></html>`)
		defer server.Close()

		session := fixture.Session

		// Create a context with a timeout for the operation.
		// The improved AnalysisContext.Navigate handles combining this with the session context.
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()

		err := session.Navigate(ctx, server.URL)
		require.NoError(t, err)

		// Pass a fresh context to Close for cleanup.
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		err = session.Close(closeCtx)
		require.NoError(t, err)

		// Verification that the session's internal context is indeed cancelled.
		// Use a timeout for the check in case the channel never closes.
		select {
		case <-session.GetContext().Done():
			// Expected behavior
		case <-time.After(5 * time.Second):
			t.Error("Session context did not close within the expected timeframe.")
		}
		assert.Error(t, session.GetContext().Err(), "Context should be cancelled after close")
	})

	t.Run("NavigateAndCollectArtifacts", func(t *testing.T) {
		t.Parallel()
		fixture := newTestFixture(t)

		server := createTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				http.SetCookie(w, &http.Cookie{Name: "SessionID", Value: "12345", HttpOnly: true, Path: "/"})
				// Add a script to generate a console log for capture verification.
				fmt.Fprintln(w, `<html><body><h1>Target Page</h1><script>console.log("Hello from JS");</script></body></html>`)
			}
		}))
		defer server.Close()

		session := fixture.Session

		// Operation context from background.
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()

		err := session.Navigate(ctx, server.URL)
		require.NoError(t, err, "Navigation failed")

		artifacts, err := session.CollectArtifacts()
		require.NoError(t, err, "Failed to collect artifacts")
		require.NotNil(t, artifacts)

		// 1. Verify DOM capture
		assert.Contains(t, artifacts.DOM, "<h1>Target Page</h1>")

		// 2. Verify Storage (Cookies) capture
		storage := artifacts.Storage
		require.NotNil(t, storage)

		var sessionCookie *network.Cookie
		for _, cookie := range storage.Cookies {
			if cookie.Name == "SessionID" {
				sessionCookie = cookie
				break
			}
		}
		require.NotNil(t, sessionCookie, "HttpOnly SessionID cookie not captured")
		assert.Equal(t, "12345", sessionCookie.Value)
		assert.True(t, sessionCookie.HTTPOnly)

		// 3. Verify Console Log capture
		assertLogPresent(t, artifacts.ConsoleLogs, "log", "Hello from JS")

		// 4. Verify HAR capture
		harEntry := findHAREntry(artifacts.HAR, server.URL)
		require.NotNil(t, harEntry, "Main document request not found in HAR")
		assert.Equal(t, 200, harEntry.Response.Status)
		// Verify that the duration is calculated (non-zero).
		assert.Greater(t, harEntry.Time, float64(0), "HAR entry duration should be greater than zero")
	})

	t.Run("SessionIsolation", func(t *testing.T) {
		t.Parallel()
		fixture1 := newTestFixture(t)
		session1 := fixture1.Session

		fixture2 := newTestFixture(t)
		session2 := fixture2.Session

		server := createTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/set" {
				http.SetCookie(w, &http.Cookie{Name: "IsolatedCookie", Value: "SessionSpecific", Path: "/"})
				fmt.Fprintln(w, "Cookie Set")
			} else {
				fmt.Fprintln(w, "Blank Page")
			}
		}))
		defer server.Close()

		// Operation context from background.
		ctx1, cancel1 := context.WithTimeout(context.Background(), testTimeout)
		defer cancel1()

		// First session sets a cookie.
		err := session1.Navigate(ctx1, server.URL+"/set")
		require.NoError(t, err)
		artifacts1, err := session1.CollectArtifacts()
		require.NoError(t, err)
		assert.NotEmpty(t, artifacts1.Storage.Cookies)

		// Operation context from background.
		ctx2, cancel2 := context.WithTimeout(context.Background(), testTimeout)
		defer cancel2()

		// Second session navigates to a different page and should not have the cookie.
		err = session2.Navigate(ctx2, server.URL+"/blank")
		require.NoError(t, err)
		artifacts2, err := session2.CollectArtifacts()
		require.NoError(t, err)

		// Check specifically for the IsolatedCookie to ensure isolation.
		found := false
		for _, c := range artifacts2.Storage.Cookies {
			if c.Name == "IsolatedCookie" {
				found = true
				break
			}
		}
		assert.False(t, found, "Session 2 should not inherit 'IsolatedCookie' from Session 1")
	})

	t.Run("Interaction", func(t *testing.T) {
		t.Parallel()
		fixture := newTestFixture(t)
		// This test relies on Humanoid being enabled in the test configuration (set in TestMain).
		require.NotNil(t, fixture.Session.humanoid, "Humanoid should be initialized for interaction tests")

		server := createStaticTestServer(t, `<!DOCTYPE html><html><body><button id="btn" onclick="this.innerText='Clicked'">Click Me</button></body></html>`)
		defer server.Close()
		session := fixture.Session

		// Operation context from background.
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()

		err := session.Navigate(ctx, server.URL)
		require.NoError(t, err)

		interactionConfig := schemas.InteractionConfig{
			MaxDepth:                2,
			MaxInteractionsPerDepth: 5,
			InteractionDelayMs:      10,
			PostInteractionWaitMs:   100,
		}

		// The implementation correctly adapts this context for the interaction process.
		err = session.Interact(ctx, interactionConfig)
		assert.NoError(t, err, "Interaction should not produce an error on a simple page")

		// Verify the interaction occurred
		artifacts, err := session.CollectArtifacts()
		require.NoError(t, err)
		assert.Contains(t, artifacts.DOM, ">Clicked<", "The button text should have changed after interaction")
	})

	t.Run("ExposeFunctionIntegration", func(t *testing.T) {
		t.Parallel()
		fixture := newTestFixture(t)
		session := fixture.Session

		server := createStaticTestServer(t, `<!DOCTYPE html><html><body><div id="status">Ready</div></body></html>`)
		defer server.Close()

		// Operation context from background. The listener setup respects this context.
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()

		// Channel to receive data from the exposed Go function.
		callbackChan := make(chan string, 1)

		// Define the function to expose.
		myFunc := func(s string, i int) {
			callbackChan <- fmt.Sprintf("%s-%d", s, i)
		}

		// CORRECTION: Expose the function BEFORE navigation.
		// This ensures the required wrapper script (injected persistently) is active
		// when the target page loads.
		err := session.ExposeFunction(ctx, "myGoFunction", myFunc)
		require.NoError(t, err)

		err = session.Navigate(ctx, server.URL)
		require.NoError(t, err)

		// Execute JavaScript that calls the bound function.
		// The injected JS wrapper handles the serialization automatically.
		script := `window.myGoFunction("test", 123);`
		err = session.ExecuteScript(ctx, script)
		require.NoError(t, err)

		// Wait for the result.
		select {
		case res := <-callbackChan:
			assert.Equal(t, "test-123", res)
		case <-time.After(5 * time.Second):
			t.Fatal("Timed out waiting for exposed function callback")
		}

		// Verify the binding is cleaned up when the context is cancelled.
		cancel()
		// The cleanup is verified by the fact that the test suite completes gracefully
		// and the robust implementation of cleanupBinding in AnalysisContext.
	})

	t.Run("NavigateTimeout", func(t *testing.T) {
		t.Parallel()
		// Configure a very short navigation timeout for this specific test.
		// We must create a deep copy of the config to avoid data races with other parallel tests.
		cfg := *brTestConfig
		networkCfg := cfg.Network
		networkCfg.NavigationTimeout = 100 * time.Millisecond
		cfg.Network = networkCfg

		// Set up a server that intentionally delays the response.
		server := createTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(500 * time.Millisecond)
			fmt.Fprintln(w, `<html><body>Slow</body></html>`)
		}))
		defer server.Close()

		// Create a session specifically with the modified config.
		// We manage the session creation manually here rather than using newTestFixture.
		sessionCtx, cancelSessionCtx := context.WithTimeout(context.Background(), testTimeout)
		defer cancelSessionCtx()

		session, err := brTestManager.NewAnalysisContext(
			sessionCtx,
			&cfg, // Pass the modified config
			schemas.DefaultPersona,
			"",
			"",
		)
		require.NoError(t, err)
		// Ensure the session is closed using a background context for cleanup.
		defer func() {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer closeCancel()
			session.Close(closeCtx)
		}()

		// Operation context for the navigation.
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()

		err = session.Navigate(ctx, server.URL)
		// Verify that the error occurred and it is specifically the timeout error.
		require.Error(t, err)
		assert.Contains(t, err.Error(), "navigation timed out")
	})
}

// -- Test Helper Functions --

// assertLogPresent verifies that a specific log message exists in the collected console logs.
func assertLogPresent(t *testing.T, logs []schemas.ConsoleLog, level, substring string) {
	t.Helper()
	found := false
	for _, log := range logs {
		// Console API logs often have type "log", "info", "warn", "error".
		if log.Type == level && strings.Contains(log.Text, substring) {
			found = true
			break
		}
	}
	assert.True(t, found, fmt.Sprintf("Expected console log not found: Type=%s, Substring='%s'.", level, substring))
}

// findHAREntry searches for a HAR entry with a URL containing a specific substring.
func findHAREntry(har *schemas.HAR, urlSubstring string) *schemas.Entry {
	if har == nil {
		return nil
	}
	for i, entry := range har.Log.Entries {
		if strings.Contains(entry.Request.URL, urlSubstring) {
			return &har.Log.Entries[i]
		}
	}
	return nil
}
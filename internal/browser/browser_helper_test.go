// internal/browser/browser_helper_test.go
package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	"github.com/xkilldash9x/scalpel-cli/internal/config"
	"github.com/xkilldash9x/scalpel-cli/internal/humanoid"
)

var (
	brTestLogger  *zap.Logger
	brTestManager *Manager
	brTestConfig  *config.Config
)

// testFixture holds components for a single, isolated browser test.
type testFixture struct {
	Session *AnalysisContext
	Config  *config.Config
}

// TestMain sets up a SINGLE, shared browser manager for all tests and handles graceful shutdown.
// This is the idiomatic Go approach for expensive, suite-wide test setup.
func TestMain(m *testing.M) {
	// Isolate the entire test main logic to its own function for clarity.
	os.Exit(testMain(m))
}

// testMain contains the core setup and teardown logic for the test suite.
func testMain(m *testing.M) int {
	var err error
	brTestLogger = getTestLogger()
	brTestLogger.Info("TestMain: Initializing suite-wide browser manager...")

	// Define a minimal, but complete, configuration for the test environment.
	brTestConfig = &config.Config{
		Browser: config.BrowserConfig{
			Headless:        true,
			DisableCache:    true,
			IgnoreTLSErrors: true, // Often useful in test environments.
			Concurrency:     4,
			Humanoid:        humanoid.DefaultConfig(),
			Debug:           true,
		},
		Network: config.NetworkConfig{
			CaptureResponseBodies: true,
			NavigationTimeout:     30 * time.Second,
			Proxy:                 config.ProxyConfig{Enabled: false},
		},
	}

	// Enable humanoid features for testing interactions.
	brTestConfig.Browser.Humanoid.Enabled = true

	// This context will govern the lifecycle of the browser process itself.
	suiteCtx, cancelSuite := context.WithCancel(context.Background())
	defer cancelSuite()

	// Add signal handling. This ensures that if the tests are interrupted (e.g., with Ctrl+C),
	// we still attempt to shut down the browser process gracefully.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		brTestLogger.Warn("Test suite interrupted by signal. Initiating shutdown...", zap.String("signal", sig.String()))
		cancelSuite()
	}()

	brTestManager, err = NewManager(suiteCtx, brTestLogger, brTestConfig)
	if err != nil {
		brTestLogger.Fatal("Failed to initialize browser manager for test suite", zap.Error(err))
	}

	// Run all the tests in the package.
	code := m.Run()

	// After the tests are done, perform a clean shutdown.
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelShutdown()
	if err := brTestManager.Shutdown(shutdownCtx); err != nil {
		brTestLogger.Error("Error during test manager shutdown", zap.Error(err))
	}

	brTestLogger.Info("TestMain: Shutdown complete.")
	return code
}

// getTestLogger creates a logger suitable for test output.
func getTestLogger() *zap.Logger {
	// Use NewDevelopment for verbose, colored output during testing.
	logger, err := zap.NewDevelopment()
	if err != nil {
		panic("failed to initialize zap logger for tests: " + err.Error())
	}
	return logger
}

// newTestFixture acquires a new session from the shared manager for an individual test.
func newTestFixture(t *testing.T) *testFixture {
	t.Helper()
	if brTestManager == nil {
		t.Fatal("brTestManager is nil. TestMain setup likely failed.")
	}

	// CORRECTION: Context Management for Parallel Tests.
	// Create a context for the session's external lifetime signal.
	sessionCtx, cancelSessionCtx := context.WithTimeout(context.Background(), 45*time.Second)

	// CRITICAL: Do NOT call t.Cleanup(cancelSessionCtx) here.
	// In parallel tests, this causes premature cancellation before the test logic runs.

	// We defer cancellation defensively in case session creation fails before the main cleanup is registered.
	creationSuccess := false
	defer func() {
		if !creationSuccess {
			cancelSessionCtx()
		}
	}()

	session, err := brTestManager.NewAnalysisContext(
		sessionCtx,
		brTestConfig,
		schemas.DefaultPersona,
		"", // No taint shim
		"", // No taint config
	)

	if err != nil {
		require.NoError(t, err, "Failed to create new analysis context")
		return nil // Should not be reached
	}
	creationSuccess = true // Mark success so the deferred cancel doesn't run.

	// We need to type assert here to access the concrete struct in tests.
	analysisContext, ok := session.(*AnalysisContext)
	require.True(t, ok, "session must be of type *AnalysisContext")

	// t.Cleanup runs automatically when the test (including parallel execution) completes.
	// This is the correct place to ensure the session is closed and the context is cancelled.
	t.Cleanup(func() {
		// Ensure the sessionCtx is cancelled when the test is truly done.
		defer cancelSessionCtx()

		// Explicitly close the session using a separate, short-lived context.
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer closeCancel()
		if err := session.Close(closeCtx); err != nil {
			// Use t.Logf in cleanup to avoid panicking.
			t.Logf("Error during session cleanup for %s: %v", session.ID(), err)
		}
	})

	return &testFixture{
		Session: analysisContext,
		Config:  brTestConfig,
	}
}

// createTestServer creates an httptest.Server.
func createTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

// createStaticTestServer creates an httptest.Server for static HTML.
func createStaticTestServer(t *testing.T, htmlContent string) *httptest.Server {
	t.Helper()
	return createTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintln(w, htmlContent)
	}))
}
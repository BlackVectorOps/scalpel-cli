package browser_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/xkilldash9x/scalpel-cli/internal/browser"
	"github.com/xkilldash9x/scalpel-cli/internal/browser/stealth"
	"github.com/xkilldash9x/scalpel-cli/internal/config"
	"github.com/xkilldash9x/scalpel-cli/internal/humanoid"
)

var (
	testLogger  *zap.Logger
	testManager *browser.Manager
	testConfig  *config.Config
)

// testFixture holds components for a single, isolated browser test.
type testFixture struct {
	Session *browser.AnalysisContext
	Config  *config.Config
}

// TestMain sets up a SINGLE, shared browser manager for all tests (Principle 1).
func TestMain(m *testing.M) {
	var err error
	testLogger = getTestLogger()

	const testConcurrency = 4

	// Configuration for the tests, matching the updated config.BrowserConfig.
	browserCfg := config.BrowserConfig{
		Headless:     true,
		DisableCache: true,
		Concurrency:  testConcurrency,
		Humanoid:     humanoid.DefaultConfig(),
		Debug:        false, // Set to true for verbose CDP logs (Principle 5).
	}

	testConfig = &config.Config{
		Browser: browserCfg,
		Network: config.NetworkConfig{
			CaptureResponseBodies: true,
			NavigationTimeout:     30 * time.Second, // Matching the updated config.NetworkConfig.
			// PostLoadWait is deprecated in the refactored code (Principle 2).
		},
	}

	// Create the manager once. Use context.Background() as the initCtx for the test suite lifetime.
	// Principle 3: Timeout for initialization.
	initCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	// Call the updated NewManager function signature.
	testManager, err = browser.NewManager(initCtx, testLogger, browserCfg)
	if err != nil {
		testLogger.Fatal("Failed to initialize browser manager for test suite", zap.Error(err))
	}

	// Run all the tests.
	code := m.Run()

	// Shut down the manager (Principle 4).
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelShutdown()
	if err := testManager.Shutdown(shutdownCtx); err != nil {
		testLogger.Error("Error during test manager shutdown", zap.Error(err))
	}

	os.Exit(code)
}

// getTestLogger creates a logger suitable for test output.
func getTestLogger() *zap.Logger {
	logger, err := zap.NewDevelopment()
	if err != nil {
		panic("failed to initialize zap logger for tests: " + err.Error())
	}
	return logger
}

// newTestFixture acquires a new session from the shared manager.
func newTestFixture(t *testing.T) (*testFixture, func()) {
	t.Helper()

	// Principle 3: Timeout for acquiring and initializing the session.
	sessionCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	session, err := testManager.NewAnalysisContext(
		sessionCtx,
		testConfig,
		stealth.DefaultPersona,
		"", // No taint template
		"", // No taint config
	)

	if err != nil {
		cancel()
		t.Fatalf("Failed to create new analysis session: %v", err)
	}

	fixture := &testFixture{
		Session: session,
		Config:  testConfig,
	}

	cleanup := func() {
		// Use a new context for cleanup in case the sessionCtx was cancelled (Principle 4).
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		session.Close(closeCtx)
		cancel()
	}

	return fixture, cleanup
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
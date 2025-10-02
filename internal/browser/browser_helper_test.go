package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync" // Import the sync package
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	"github.com/xkilldash9x/scalpel-cli/internal/browser/humanoid"
	"github.com/xkilldash9x/scalpel-cli/internal/browser/session"
	"github.com/xkilldash9x/scalpel-cli/internal/config"
)

const (
	// A generous timeout for individual test operations.
	testTimeout     = 30 * time.Second
	shutdownTimeout = 30 * time.Second
	initTimeout     = 5 * time.Minute // For manager initialization (includes browser installation).
)

// FIX: The technical review on Go HTTP testing strongly advised against using
// a shared manager (initialized in TestMain) due to the high risk of state
// leakage between tests. A stateful manager can lead to flaky, order-dependent
// tests that are difficult to debug.
//
// The following code removes the TestMain and global suiteManager, adopting the
// "gold standard" pattern of creating a new, fully isolated Manager for each
// test. This guarantees test hermeticity and reliability at the cost of some
// performance, a trade off that aligns with best practices for robust testing.
type testFixture struct {
	Session      *session.Session
	Config       *config.Config
	Manager      *Manager
	Logger       *zap.Logger
	FindingsChan chan schemas.Finding
	// TestWG tracks concurrent operations launched by the test.
	// The framework waits for this WG before tearing down the browser session.
	// This implements the Graceful Teardown pattern.
	TestWG *sync.WaitGroup
}

// fixtureConfigurator allows for customizing the test configuration on a
// per fixture basis.
type fixtureConfigurator func(*config.Config)

func getTestLogger() *zap.Logger {
	// Using a development config provides detailed, human readable logs during testing.
	logger, err := zap.NewDevelopment()
	if err != nil {
		panic("failed to initialize zap logger for tests: " + err.Error())
	}
	return logger
}

func createTestConfig() *config.Config {
	// This configuration is optimized for automated testing environments.
	humanoidCfg := humanoid.DefaultConfig()
	// Speed up humanoid actions for faster test execution.
	humanoidCfg.ClickHoldMinMs = 5
	humanoidCfg.ClickHoldMaxMs = 15
	humanoidCfg.KeyHoldMeanMs = 10.0

	cfg := &config.Config{
		Browser: config.BrowserConfig{
			Headless:        true, // Always run headless in CI/tests.
			DisableCache:    true,
			IgnoreTLSErrors: true,
			Humanoid:        humanoidCfg,
			Debug:           true,
		},
		Network: config.NetworkConfig{
			CaptureResponseBodies: true,
			NavigationTimeout:     45 * time.Second,
			Proxy:                 config.ProxyConfig{Enabled: false},
			IgnoreTLSErrors:       true,
		},
		IAST: config.IASTConfig{Enabled: false},
	}
	cfg.Browser.Humanoid.Enabled = true
	return cfg
}

// newTestFixture creates a fully self contained test environment, including a new
// Manager and a new Session. It ensures all resources are gracefully torn down
// when the test completes using `t.Cleanup`.
func newTestFixture(t *testing.T, configurators ...fixtureConfigurator) *testFixture {
	t.Helper()

	cfg := createTestConfig()
	for _, configurator := range configurators {
		configurator(cfg)
	}

	logger := getTestLogger().With(zap.String("test", t.Name()))

	// Initialize the WaitGroup for tracking test operations.
	var testWG sync.WaitGroup

	// -- Manager Creation --
	initCtx, initCancel := context.WithTimeout(context.Background(), initTimeout)
	defer initCancel()
	manager, err := NewManager(initCtx, logger, cfg)
	require.NoError(t, err, "Failed to create a new test-specific manager")

	// --- t.Cleanup Registration (LIFO Order) ---

	// 1. Registered First: Runs Last (Manager Shutdown)
	t.Cleanup(func() {
		// As defense in depth, wait here too, in case session creation failed
		// but the test still launched background work.
		testWG.Wait()

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer shutdownCancel()
		logger.Debug("Shutting down manager.")
		if shutdownErr := manager.Shutdown(shutdownCtx); shutdownErr != nil {
			t.Logf("Warning: error during manager shutdown in cleanup: %v", shutdownErr)
		}
	})

	findingsChan := make(chan schemas.Finding, 50)
	// 2. Registered Second: Runs Second to Last
	t.Cleanup(func() { close(findingsChan) })

	// -- Session Creation --
	testCtx, testCancel := context.WithTimeout(t.Context(), testTimeout)
	defer testCancel()

	sessionInterface, err := manager.NewAnalysisContext(
		testCtx,
		cfg,
		schemas.DefaultPersona,
		"",
		"",
		findingsChan,
	)
	require.NoError(t, err, "Failed to create new analysis context (session)")

	sess, ok := sessionInterface.(*session.Session)
	require.True(t, ok, "session must be of type *session.Session")

	// 3. Registered Last: Runs First (Graceful Teardown and Session Close)
	// FIX: This implements the Global Synchronization pattern.
	t.Cleanup(func() {
		logger.Debug("Test function completed. Waiting for TestWG (Graceful Teardown).")
		// CRITICAL: Block until all concurrent test operations are done.
		testWG.Wait()
		logger.Debug("TestWG complete. Proceeding to close session.")

		// Once all tracked work is done, it is safe to close the browser.
		closeCtx, closeCancel := context.WithTimeout(context.Background(), shutdownTimeout/3)
		defer closeCancel()
		if closeErr := sess.Close(closeCtx); closeErr != nil {
			t.Logf("Warning: Error during session close in cleanup: %v", closeErr)
		}
	})

	return &testFixture{
		Session:      sess,
		Config:       cfg,
		Manager:      manager,
		Logger:       logger,
		FindingsChan: findingsChan,
		TestWG:       &testWG, // Return the WaitGroup
	}
}

// createTestServer is a helper for creating a standard httptest.Server that is
// automatically closed when the test finishes.
func createTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	// t.Cleanup ensures server.Close is called, preventing resource leaks.
	t.Cleanup(server.Close)
	return server
}

// createStaticTestServer is a convenience wrapper around createTestServer for
// serving a simple, static HTML string.
func createStaticTestServer(t *testing.T, htmlContent string) *httptest.Server {
	t.Helper()
	return createTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintln(w, htmlContent)
	}))
}

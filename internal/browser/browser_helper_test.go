package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	"github.com/xkilldash9x/scalpel-cli/internal/config"
	"github.com/xkilldash9x/scalpel-cli/internal/humanoid"
)

// FIX: Change variable names to avoid shadowing the 'browser' package name.
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
func TestMain(m *testing.M) {
	var err error
	brTestLogger = getTestLogger()
	brTestLogger.Info("TestMain: START")

	browserCfg := config.BrowserConfig{
		Headless:     true,
		DisableCache: true,
		Concurrency:  4,
		Humanoid:     humanoid.DefaultConfig(),
		Debug:        true,
	}
	// FIX: Use a fully initialized config.
	brTestConfig = &config.Config{
		Browser: browserCfg,
		Network: config.NetworkConfig{
			CaptureResponseBodies: true,
			NavigationTimeout:     30 * time.Second,
			Proxy:                 config.ProxyConfig{Enabled: false},
		},
	}

	suiteCtx, cancel := context.WithCancel(context.Background())

	// FIX: Pass the entire root config `brTestConfig` to NewManager.
	brTestManager, err = NewManager(suiteCtx, brTestLogger, brTestConfig)
	if err != nil {
		cancel()
		brTestLogger.Fatal("Failed to initialize browser manager for test suite", zap.Error(err))
	}

	code := m.Run()

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelShutdown()
	if err := brTestManager.Shutdown(shutdownCtx); err != nil {
		brTestLogger.Error("Error during test manager shutdown", zap.Error(err))
	}

	cancel()
	brTestLogger.Info("TestMain: END")
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

// newTestFixture acquires a new session from the shared manager for an individual test.
func newTestFixture(t *testing.T) *testFixture {
	t.Helper()
	if brTestManager == nil {
		t.Fatal("brTestManager is nil. TestMain setup likely failed.")
	}

	sessionCtx, cancelSessionCtx := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancelSessionCtx)

	// FIX: Use schemas.DefaultPersona
	session, err := brTestManager.NewAnalysisContext(
		sessionCtx,
		brTestConfig,
		schemas.DefaultPersona,
		"", // No taint shim
		"", // No taint config
	)
	require.NoError(t, err)

	// We need to type-assert here to access the concrete struct in tests.
	analysisContext, ok := session.(*AnalysisContext)
	require.True(t, ok, "session must be of type *AnalysisContext")

	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer closeCancel()
		session.Close(closeCtx)
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
// internal/jsoncompare/main_test.go
package jsoncompare

import (
	"os"
	"testing"

	"github.com/xkilldash9x/scalpel-cli/internal/config"
	"github.com/xkilldash9x/scalpel-cli/internal/observability"
)

// TestMain will execute once before any other tests in the jsoncompare package.
func TestMain(m *testing.M) {
	// Create a default logger config for testing.
	cfg := config.LoggerConfig{
		Level:       "debug",
		Format:      "console",
		AddSource:   true,
		ServiceName: "jsoncompare",
	}
	// Initialize the global logger before running tests.
	observability.InitializeLogger(cfg)
	// m.Run() executes the tests. The exit code is passed to os.Exit.
	os.Exit(m.Run())
}

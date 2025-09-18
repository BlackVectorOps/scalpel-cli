// internal/observability/logger_test.go
package observability

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xkilldash9x/scalpel-cli/internal/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore" // FIX: Import zapcore
)

// -- Test Helper Functions --

// FIX: Removed the racy captureOutput helper function.

// setupTestLogger initializes the global logger to write to a buffer for testing.
func setupTestLogger(cfg config.LoggerConfig) *bytes.Buffer {
	buf := new(bytes.Buffer)
	// Wrap the buffer in a WriteSyncer.
	writer := zapcore.AddSync(buf)
	// Call the internal initializer, directing console output to the buffer.
	initializeLogger(cfg, writer)
	return buf
}

// resetGlobalLogger is critical for ensuring test isolation, as the logger
// is a global singleton. We must reset it before each test.
func resetGlobalLogger() {
	// Reset the sync.Once so InitializeLogger can be called again.
	once = sync.Once{}
	// Set the atomic pointer to nil.
	globalLogger.Store(nil)
}

// -- Test Cases --

func TestInitializeLogger(t *testing.T) {

	t.Run("should initialize console logger with colors", func(t *testing.T) {
		resetGlobalLogger()
		// FIX: Removed captureOutput calls.

		cfg := config.LoggerConfig{
			Level:       "debug",
			Format:      "console",
			ServiceName: "TestService",
			Colors: config.ColorConfig{ // -- testing our color configuration --
				Info: "green",
			},
		}

		// FIX: Use the new helper to initialize and capture output.
		buf := setupTestLogger(cfg)

		logger := GetLogger()
		logger.Info("This is a test message.")
		Sync() // -- ensure the log is flushed --

		output := buf.String()
		assert.Contains(t, output, "INFO", "Output should contain the log level")
		assert.Contains(t, output, "This is a test message.", "Output should contain the message")
		assert.Contains(t, output, colorGreen, "Info level should be colorized green")
		assert.Contains(t, output, colorReset, "Output should contain the reset color code")
	})

	t.Run("should initialize json logger", func(t *testing.T) {
		resetGlobalLogger()
		// FIX: Removed captureOutput calls.

		cfg := config.LoggerConfig{
			Level:       "info",
			Format:      "json",
			ServiceName: "JSONTest",
		}

		// FIX: Use the new helper.
		buf := setupTestLogger(cfg)

		logger := GetLogger()
		logger.Warn("This is a JSON message.", zap.String("key", "value"))
		Sync()

		// -- the output should be a valid JSON object --
		var logEntry map[string]interface{}
		err := json.Unmarshal(buf.Bytes(), &logEntry)
		require.NoError(t, err, "Log output should be valid JSON")

		// FIX: The JSON encoder is configured to use CapitalLevelEncoder, so the level is "WARN", not "warn".
		assert.Equal(t, "WARN", logEntry["level"])
		assert.Equal(t, "JSONTest", logEntry["logger"])
		assert.Equal(t, "This is a JSON message.", logEntry["msg"])
		assert.Equal(t, "value", logEntry["key"])
	})

	t.Run("should write to a log file if configured", func(t *testing.T) {
		resetGlobalLogger()
		// -- create a temporary file for the log output --
		tmpFile, err := ioutil.TempFile("", "logger-test-*.log")
		require.NoError(t, err)
		defer os.Remove(tmpFile.Name())

		cfg := config.LoggerConfig{
			Level:   "debug",
			Format:  "json",
			LogFile: tmpFile.Name(),
			MaxSize: 1, // 1 MB
		}
		// We can use InitializeLogger here as we are testing file output, not console output.
		InitializeLogger(cfg)
		logger := GetLogger()
		logger.Error("This should go to the file.")
		Sync()

		content, err := ioutil.ReadFile(tmpFile.Name())
		require.NoError(t, err)
		assert.Contains(t, string(content), "This should go to the file.", "Log file should contain the message")
	})

	t.Run("should only initialize once", func(t *testing.T) {
		resetGlobalLogger()
		// FIX: Removed captureOutput calls.

		// -- first initialization --
		// Use console format for easier string comparison of the service name.
		cfg1 := config.LoggerConfig{Level: "info", Format: "console", ServiceName: "First"}
		// FIX: Use the helper for the first initialization.
		buf1 := setupTestLogger(cfg1)
		logger1 := GetLogger()

		// -- second, should be ignored --
		cfg2 := config.LoggerConfig{Level: "debug", Format: "console", ServiceName: "Second"}
		// FIX: Attempt initialization again. once.Do prevents it.
		// The global logger remains configured with buf1. buf2 will be empty.
		buf2 := setupTestLogger(cfg2)
		logger2 := GetLogger()

		// -- check that the logger is the same instance with the first config --
		assert.Equal(t, logger1, logger2)
		logger2.Info("test message") // This writes to buf1.
		Sync()

		// The service name should be "First", not "Second"
		output := buf1.String()
		assert.True(t, strings.Contains(output, "First"))
		assert.True(t, strings.Contains(output, "test message"))
		assert.False(t, strings.Contains(output, "Second"))
		// Ensure the second buffer remains empty.
		assert.Empty(t, buf2.String())
	})
}

func TestGetLogger(t *testing.T) {
	t.Run("should return a fallback logger if not initialized", func(t *testing.T) {
		resetGlobalLogger()
		// -- we do not call InitializeLogger() here --
		logger := GetLogger()
		require.NotNil(t, logger)

		// The fallback logger is a development logger named "fallback".
		// We can't easily assert its exact type, but we can check its behavior.
		// A non-nil check is a good indicator it worked.
	})

	t.Run("should return the global logger after initialization", func(t *testing.T) {
		resetGlobalLogger()
		cfg := config.LoggerConfig{Level: "info", ServiceName: "GlobalTest"}
		InitializeLogger(cfg)

		logger := GetLogger()
		// The pointer to the logger instance should be the same as the one stored.
		assert.Equal(t, globalLogger.Load(), logger)
	})
}
package llmclient

import (
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/xkilldash9x/scalpel-cli/internal/config"
)

// setupTestLogger is a helper to create a zap logger for testing with an observer.
func setupTestLogger(t *testing.T) *zap.Logger {
	t.Helper()
	core, _ := observer.New(zap.DebugLevel)
	return zap.New(core)
}

// getValidLLMConfig returns a valid LLMModelConfig for testing purposes.
func getValidLLMConfig() config.LLMModelConfig {
	return config.LLMModelConfig{
		Provider:    config.ProviderGemini,
		APIKey:      "test-api-key",
		Model:       "test-model",
		APITimeout:  5 * time.Second,
		Temperature: 0.7,
		TopP:        0.9,
		TopK:        50,
	}
}

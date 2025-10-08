package stealth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	"github.com/xkilldash9x/scalpel-cli/internal/mocks"
)

func TestApplyEvasions(t *testing.T) {
	ctx := context.Background()
	mockSession := mocks.NewMockSessionContext()
	persona := schemas.DefaultPersona

	t.Run("Logging with Non-Empty EvasionsJS", func(t *testing.T) {

		// Setup logger observer
		core, observedLogs := observer.New(zap.DebugLevel)
		logger := zap.New(core)

		// Temporarily ensure EvasionsJS is not empty (White-box testing access)
		originalEvasionsJS := EvasionsJS
		EvasionsJS = "some script"
		defer func() { EvasionsJS = originalEvasionsJS }()

		err := ApplyEvasions(ctx, mockSession, persona, logger)
		assert.NoError(t, err)

		// Assert logs
		logs := observedLogs.All()
		require.Len(t, logs, 2)
		assert.Contains(t, logs[0].Message, "Applying stealth configuration (Pure Go mode).")
		assert.Contains(t, logs[1].Message, "Note: EvasionsJS (navigator spoofing) is skipped")
	})

	t.Run("Logging with Empty EvasionsJS", func(t *testing.T) {
		core, observedLogs := observer.New(zap.DebugLevel)
		logger := zap.New(core)

		// Temporarily ensure EvasionsJS is empty
		originalEvasionsJS := EvasionsJS
		EvasionsJS = ""
		defer func() { EvasionsJS = originalEvasionsJS }()

		err := ApplyEvasions(ctx, mockSession, persona, logger)
		assert.NoError(t, err)

		// Assert logs - the skip message should not appear
		logs := observedLogs.All()
		require.Len(t, logs, 1)
		assert.Contains(t, logs[0].Message, "Applying stealth configuration (Pure Go mode).")
	})

	t.Run("Nil Logger", func(t *testing.T) {
		// Ensure it doesn't panic if the logger is nil.
		assert.NotPanics(t, func() {
			err := ApplyEvasions(ctx, mockSession, persona, nil)
			assert.NoError(t, err)
		})
	})
}

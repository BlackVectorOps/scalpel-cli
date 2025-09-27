// internal/browser/session/timing_test.go
package session

// We place this test inside the 'session' package (not 'session_test') to access unexported timing functions.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSimulateClickTiming(t *testing.T) {
	minMs := 50
	maxMs := 150
	ctx := context.Background()

	startTime := time.Now()
	err := simulateClickTiming(ctx, minMs, maxMs)
	duration := time.Since(startTime)

	assert.NoError(t, err)

	// Check if the duration is within the expected range (allowing some minor overhead)
	assert.GreaterOrEqual(t, duration, time.Duration(minMs)*time.Millisecond)
	// Allow a small buffer (e.g., 50ms) for overhead
	assert.LessOrEqual(t, duration, time.Duration(maxMs+50)*time.Millisecond)
}

func TestSimulateClickTiming_InvalidArgs(t *testing.T) {
	startTime := time.Now()
	// Test zero values, negative values, and max < min
	simulateClickTiming(context.Background(), 0, 0)
	simulateClickTiming(context.Background(), -10, 10)
	simulateClickTiming(context.Background(), 100, 50)
	duration := time.Since(startTime)

	// Should return almost immediately
	assert.Less(t, duration, 20*time.Millisecond, "Invalid arguments should result in immediate return")
}

func TestSimulateClickTiming_Cancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	startTime := time.Now()
	// Long duration that should be interrupted
	err := simulateClickTiming(ctx, 500, 1000)
	duration := time.Since(startTime)

	assert.Error(t, err)
	assert.Equal(t, context.Canceled, err)

	// Check duration respected the cancellation time
	assert.Less(t, duration, 150*time.Millisecond)
}

func TestSimulateTyping(t *testing.T) {
	text := "hello" // 5 characters
	holdMeanMs := 60.0
	ctx := context.Background()

	startTime := time.Now()
	err := simulateTyping(ctx, text, holdMeanMs)
	duration := time.Since(startTime)

	assert.NoError(t, err)

	// Expected duration is roughly (len(text) * holdMeanMs) + (len(text)-1 * interKeyMeanMs (100ms))
	// (5 * 60) + (4 * 100) = 700ms
	// Due to variance, we check a broad range.
	minExpected := 5 * 20 * time.Millisecond // Absolute minimum physical time (20ms/key)
	maxExpected := 1500 * time.Millisecond   // Generous upper bound

	assert.GreaterOrEqual(t, duration, minExpected)
	assert.LessOrEqual(t, duration, maxExpected)
}

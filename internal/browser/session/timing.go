// browser/session/timing.go
package session

// This file contains timing simulation utilities used by the Session.

import (
	"context"
	"math/rand"
	"sync"
	"time"
)

// rngPool manages synchronized random number generators.
var rngPool = sync.Pool{
	New: func() interface{} {
		return rand.New(rand.NewSource(time.Now().UnixNano()))
	},
}

func getRNG() *rand.Rand {
	return rngPool.Get().(*rand.Rand)
}

func putRNG(r *rand.Rand) {
	rngPool.Put(r)
}

// simulateTyping simulates the timing of typing a string character by character.
func simulateTyping(ctx context.Context, text string, holdMeanMs float64) error {
	if holdMeanMs <= 0 || len(text) == 0 {
		return nil
	}

	rng := getRNG()
	defer putRNG(rng)

	// Define realistic variances based on typical human typing patterns.
	holdVariance := 15.0       // Variance for key hold time (ms).
	interKeyMeanMs := 100.0    // Mean time between keystrokes (ms).
	interKeyVariance := 40.0   // Variance between keystrokes (ms).

	for i := range text {
		// 1. Simulate Key Hold time (using normal distribution approximation).
		holdMs := rand.NormFloat64()*holdVariance + holdMeanMs
		if holdMs < 20 { // Minimum physical duration.
			holdMs = 20
		}
		if err := hesitate(ctx, time.Duration(holdMs)*time.Millisecond); err != nil {
			return err
		}

		// Don't delay after the very last character.
		if i == len(text)-1 {
			break
		}

		// 2. Simulate Inter-Key delay.
		interKeyMs := rand.NormFloat64()*interKeyVariance + interKeyMeanMs
		if interKeyMs < 30 { // Minimum delay between keys.
			interKeyMs = 30
		}
		if err := hesitate(ctx, time.Duration(interKeyMs)*time.Millisecond); err != nil {
			return err
		}
	}
	return nil
}

// hesitate pauses execution, respecting the context cancellation.
func hesitate(ctx context.Context, duration time.Duration) error {
	select {
	case <-time.After(duration):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// simulateClickTiming pauses for the duration of a mouse click (press and release).
func simulateClickTiming(ctx context.Context, minMs, maxMs int) error {
	// If timings are 0 or invalid, return immediately.
	if minMs <= 0 || maxMs <= 0 || minMs > maxMs {
		return nil
	}

	rng := getRNG()
	defer putRNG(rng)

	// Calculate random duration within the bounds.
	rangeMs := maxMs - minMs
	if rangeMs < 0 {
		rangeMs = 0
	}
	durationMs := minMs + rng.Intn(rangeMs+1)

	return hesitate(ctx, time.Duration(durationMs)*time.Millisecond)
}

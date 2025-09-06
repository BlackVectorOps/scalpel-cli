// -- pkg/humanoid/behavior.go --
package humanoid

import (
	"context"
	"math"
	"time"

	"github.com/chromedp/chromedp"
	"go.uber.org/zap"
)

// CognitivePause simulates a pause with subtle, noisy cursor movements (idling behavior).
// It also handles fatigue recovery.
func (h *Humanoid) CognitivePause(ctx context.Context, meanMs, stdDevMs float64) error {
	h.mu.Lock()
	// Fatigue makes cognitive processes slower.
	fatigueFactor := 1.0 + h.fatigueLevel
	rng := h.rng
	h.mu.Unlock()

	// Calculate the duration of the pause.
	duration := time.Duration(fatigueFactor*(meanMs+rng.NormFloat64()*stdDevMs)) * time.Millisecond
	if duration <= 0 {
		return nil
	}

	// Recover fatigue during the pause.
	h.recoverFatigue(duration)

	// For longer pauses (> 100ms), simulate the cursor idling (Hesitate).
	if duration > 100*time.Millisecond {
		return h.Hesitate(duration).Do(ctx)
	}

	// For shorter pauses, a simple sleep is sufficient (no mouse movement).
	return h.pause(ctx, duration)
}

// Hesitate simulates a pause with subtle, noisy cursor movements.
func (h *Humanoid) Hesitate(duration time.Duration) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		h.mu.Lock()
		startPos := h.currentPos
		h.mu.Unlock()

		startTime := time.Now()
		// Use context-aware loop termination check.
		for time.Since(startTime) < duration && ctx.Err() == nil {
			// Small, random movements.
			h.mu.Lock()
			finalPerturbedPoint := startPos.Add(Vector2D{
				X: (h.rng.Float64() - 0.5) * 5,
				Y: (h.rng.Float64() - 0.5) * 5,
			})
			h.mu.Unlock()

			// CORRECTION: Use the correct high-level function chromedp.MouseMove.
			moveAction := chromedp.MouseMove(finalPerturbedPoint.X, finalPerturbedPoint.Y)
			if err := moveAction.Do(ctx); err != nil {
				h.logger.Debug("Humanoid: Hesitation movement failed", zap.Error(err))
				// If context is cancelled, return the error immediately.
				if ctx.Err() != nil {
					return ctx.Err()
				}
				// Otherwise, we can ignore this non-critical error.
			}

			// Update internal position tracker after successful move.
			h.mu.Lock()
			h.currentPos = finalPerturbedPoint
			h.mu.Unlock()

			// Wait a bit before the next micro movement.
			h.mu.Lock()
			// Ensure Intn argument is positive.
			randPart := 0
			if 100 > 0 {
				randPart = h.rng.Intn(100)
			}
			pauseDuration := time.Duration(50+randPart) * time.Millisecond
			h.mu.Unlock()

			// Adjust pause duration if it exceeds the remaining total duration.
			if time.Since(startTime)+pauseDuration > duration {
				pauseDuration = duration - time.Since(startTime)
			}

			// Ensure duration is positive before sleeping.
			if pauseDuration <= 0 {
				break
			}

			// Use context-aware sleep instead of time.Sleep.
			if err := sleepContext(ctx, pauseDuration); err != nil {
				return err
			}
		}
		return nil
	})
}

// applyGaussianNoise adds high frequency tremor.
func (h *Humanoid) applyGaussianNoise(point Vector2D) Vector2D {
	h.mu.Lock()
	defer h.mu.Unlock()
	// Strength varies slightly randomly around the dynamic config value.
	strength := h.dynamicConfig.GaussianStrength * (0.5 + h.rng.Float64())
	pX := h.rng.NormFloat64() * strength
	pY := h.rng.NormFloat64() * strength
	return Vector2D{X: point.X + pX, Y: point.Y + pY}
}

// applyFatigueEffects adjusts the dynamic configuration based on the current fatigue level.
func (h *Humanoid) applyFatigueEffects() {
	// Fatigue factor (1.0 when rested, up to 2.0 when exhausted).
	fatigueFactor := 1.0 + h.fatigueLevel

	// Apply effects: movements become less precise and slower.
	h.dynamicConfig.GaussianStrength = h.baseConfig.GaussianStrength * fatigueFactor
	h.dynamicConfig.PerlinAmplitude = h.baseConfig.PerlinAmplitude * fatigueFactor
	h.dynamicConfig.FittsA = h.baseConfig.FittsA * fatigueFactor
	// Typing accuracy decreases more sharply (e.g., factor of up to 3x at max fatigue).
	h.dynamicConfig.TypoRate = h.baseConfig.TypoRate * (1.0 + h.fatigueLevel*2.0)
	h.dynamicConfig.TypoRate = math.Min(0.25, h.dynamicConfig.TypoRate)
}

// updateFatigue modifies the fatigue level based on action intensity and adjusts the dynamic configuration.
func (h *Humanoid) updateFatigue(intensity float64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Intensity represents the cognitive/physical load (normalized 0.0 to 1.0 typically)
	increase := h.baseConfig.FatigueIncreaseRate * intensity
	h.fatigueLevel += increase
	h.fatigueLevel = math.Min(1.0, h.fatigueLevel) // Clamp at 1.0

	h.applyFatigueEffects()
}

// recoverFatigue simulates recovery during pauses or inactivity.
func (h *Humanoid) recoverFatigue(duration time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Recovery is proportional to the duration of the pause.
	recovery := h.baseConfig.FatigueRecoveryRate * duration.Seconds()
	h.fatigueLevel -= recovery
	h.fatigueLevel = math.Max(0.0, h.fatigueLevel) // Clamp at 0.0

	h.applyFatigueEffects()
}
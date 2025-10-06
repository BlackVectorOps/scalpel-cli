package humanoid

import (
	"context"
	"math"
	"time"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"
)

// CognitivePause is the public entry point for pausing. It acquires the lock.
func (h *Humanoid) CognitivePause(ctx context.Context, meanMs, stdDevMs float64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cognitivePause(ctx, meanMs, stdDevMs)
}

// cognitivePause is the internal, non-locking implementation. It assumes the caller holds the lock.
func (h *Humanoid) cognitivePause(ctx context.Context, meanMs, stdDevMs float64) error {
	fatigueFactor := 1.0 + h.fatigueLevel
	rng := h.rng

	duration := time.Duration(fatigueFactor*(meanMs+rng.NormFloat64()*stdDevMs)) * time.Millisecond
	if duration <= 0 {
		return nil
	}
	// Call internal recoverFatigue, which does not lock.
	h.recoverFatigue(duration)

	// For pauses, simulate active idling (hesitation/drift).
	// We do this for most pauses now, as humans rarely stay perfectly still.
	if duration > 20*time.Millisecond {
		// Call the internal, non-locking version of Hesitate.
		return h.hesitate(ctx, duration)
	}

	return h.executor.Sleep(ctx, duration)
}

// Hesitate simulates a user pausing and acquires a lock.
// This is kept for compatibility but internal calls should use hesitate().
func (h *Humanoid) Hesitate(ctx context.Context, duration time.Duration) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hesitate(ctx, duration)
}

// hesitate is the internal, non-locking implementation of cursor idling using smooth Perlin noise drift.
func (h *Humanoid) hesitate(ctx context.Context, duration time.Duration) error {
	startPos := h.currentPos
	// Get the current button state to maintain it during hesitation (e.g., crucial for dragging/clicking).
	currentButtons := h.calculateButtonsBitfield(h.currentButtonState)
	startTime := time.Now()

	// Define parameters for the idle drift.
	// Use dynamic config for amplitude, influenced by fatigue.
	driftAmplitude := h.dynamicConfig.PerlinAmplitude * 1.5 // Slightly increased amplitude for idling vs trajectory waver.
	const driftFrequency = 0.5                               // How fast the direction changes (Hz).
	const updateInterval = 20 * time.Millisecond             // How often we update the position (50Hz).

	for time.Since(startTime) < duration {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Calculate drift using Perlin noise based on time elapsed since the start of hesitation.
		timeElapsed := time.Since(startTime).Seconds()
		drift := Vector2D{
			X: h.noiseX.Noise1D(timeElapsed*driftFrequency) * driftAmplitude,
			Y: h.noiseY.Noise1D(timeElapsed*driftFrequency) * driftAmplitude,
		}

		targetPos := startPos.Add(drift)

		// Apply Gaussian noise (tremor) on top of the drift.
		finalPos := h.applyGaussianNoise(targetPos)

		// Dispatch the movement event.
		eventData := schemas.MouseEventData{
			Type:    schemas.MouseMove,
			X:       finalPos.X,
			Y:       finalPos.Y,
			Button:  schemas.ButtonNone,
			Buttons: currentButtons,
		}

		if err := h.executor.DispatchMouseEvent(ctx, eventData); err != nil {
			return err
		}

		h.currentPos = finalPos

		// Determine the sleep duration for this iteration.
		pauseDuration := updateInterval
		// Ensure we don't overshoot the total duration.
		if time.Since(startTime)+pauseDuration > duration {
			pauseDuration = duration - time.Since(startTime)
		}
		if pauseDuration <= 0 {
			break
		}

		if err := h.executor.Sleep(ctx, pauseDuration); err != nil {
			return err
		}
	}
	return nil
}

// applyGaussianNoise adds high-frequency "tremor" to a mouse coordinate.
// This is an internal helper that assumes the lock is held.
func (h *Humanoid) applyGaussianNoise(point Vector2D) Vector2D {
	strength := h.dynamicConfig.GaussianStrength * (0.5 + h.rng.Float64())
	pX := h.rng.NormFloat64() * strength
	pY := h.rng.NormFloat64() * strength

	return Vector2D{X: point.X + pX, Y: point.Y + pY}
}

// applyClickNoise adds small displacement noise that occurs during the physical action of clicking/grabbing.
// This models the slight involuntary movement when muscles tense for a click, often biased downwards.
// This is an internal helper that assumes the lock is held.
func (h *Humanoid) applyClickNoise(point Vector2D) Vector2D {
	// Strength is influenced by the base configuration and fatigue (via dynamicConfig).
	strength := h.dynamicConfig.ClickNoise * (0.5 + h.rng.Float64())

	pX := h.rng.NormFloat64() * strength * 0.5
	// Use Abs() to ensure the Y bias is positive (downwards).
	pY := math.Abs(h.rng.NormFloat64() * strength)

	return Vector2D{X: point.X + pX, Y: point.Y + pY}
}

// applyFatigueEffects adjusts the dynamic configuration based on the current fatigue level.
// This is an internal helper that assumes the lock is held.
func (h *Humanoid) applyFatigueEffects() {
	fatigueLevel := h.fatigueLevel
	fatigueFactor := 1.0 + fatigueLevel

	h.dynamicConfig.GaussianStrength = h.baseConfig.GaussianStrength * fatigueFactor
	h.dynamicConfig.PerlinAmplitude = h.baseConfig.PerlinAmplitude * fatigueFactor
	h.dynamicConfig.FittsA = h.baseConfig.FittsA * fatigueFactor

	// Fatigue increases click noise (less precise control).
	h.dynamicConfig.ClickNoise = h.baseConfig.ClickNoise * fatigueFactor

	// Fatigue affects motor control parameters (Omega/Zeta).
	// When fatigued, movement is slower (lower Omega) and potentially less stable (slightly lower Zeta).
	h.dynamicConfig.Omega = h.baseConfig.Omega * (1.0 - fatigueLevel*0.3)
	h.dynamicConfig.Zeta = h.baseConfig.Zeta * (1.0 - fatigueLevel*0.1)

	h.dynamicConfig.TypoRate = h.baseConfig.TypoRate * (1.0 + fatigueLevel*2.0)
	h.dynamicConfig.TypoRate = math.Min(0.25, h.dynamicConfig.TypoRate)
}

// updateFatigue modifies the fatigue level. It assumes the lock is held.
func (h *Humanoid) updateFatigue(intensity float64) {
	increase := h.baseConfig.FatigueIncreaseRate * intensity
	h.fatigueLevel += increase
	h.fatigueLevel = math.Min(1.0, h.fatigueLevel)

	h.applyFatigueEffects()
}

// recoverFatigue simulates recovery from fatigue. It assumes the lock is held.
func (h *Humanoid) recoverFatigue(duration time.Duration) {
	recovery := h.baseConfig.FatigueRecoveryRate * duration.Seconds()
	h.fatigueLevel -= recovery
	h.fatigueLevel = math.Max(0.0, h.fatigueLevel)

	h.applyFatigueEffects()
}

// calculateButtonsBitfield converts the internal MouseButton state into the standard bitfield representation.
// This is a stateless helper and does not require a lock.
func (h *Humanoid) calculateButtonsBitfield(buttonState schemas.MouseButton) int64 {
	var buttons int64
	switch buttonState {
	case schemas.ButtonLeft:
		buttons = 1
	case schemas.ButtonRight:
		buttons = 2
	case schemas.ButtonMiddle:
		buttons = 4
	}
	return buttons
}
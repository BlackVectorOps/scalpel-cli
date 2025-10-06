package humanoid

import (
	"context"
	"math"
	"time"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	"go.uber.org/zap"
)

// IntelligentClick performs a human-like click action on a selector.
func (h *Humanoid) IntelligentClick(ctx context.Context, selector string, opts *InteractionOptions) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// 1. Move to the target element.
	// Call the internal, non-locking move method.
	// This handles ensureVisible, movement simulation, and terminal pause.
	if err := h.moveToSelector(ctx, selector, opts); err != nil {
		return err
	}

	// 2. Cognitive pause before the action (final verification).
	if err := h.cognitivePause(ctx, 50, 20); err != nil {
		return err
	}

	// 3. Mouse Down (Press).
	// Apply click noise (physical displacement) before the press event.
	currentPos := h.currentPos
	clickPos := h.applyClickNoise(currentPos)

	mouseDownData := schemas.MouseEventData{
		Type:       schemas.MousePress,
		X:          clickPos.X,
		Y:          clickPos.Y,
		Button:     schemas.ButtonLeft,
		ClickCount: 1,
		Buttons:    1, // Bitfield: 1 indicates the left button is now pressed.
	}
	if err := h.executor.DispatchMouseEvent(ctx, mouseDownData); err != nil {
		return err
	}

	h.currentPos = clickPos // Update position after noise application
	h.currentButtonState = schemas.ButtonLeft

	// 4. Hold Duration and Click Slip/Tremor.
	holdDuration := h.calculateClickHoldDuration()

	// Simulate subtle movement (tremor or slip) while the button is held down.
	// We use the internal hesitate function, which correctly maintains the button state (pressed).
	if err := h.hesitate(ctx, holdDuration); err != nil {
		// If hesitation fails (e.g., context cancelled), we must ensure the mouse is released.
		h.logger.Warn("Humanoid: Click hold hesitation interrupted, attempting cleanup (mouse release)", zap.Error(err))
		// Use background context for cleanup as the original context might be cancelled.
		h.releaseMouse(context.Background())
		return err
	}

	// 5. Mouse Up (Release).
	// Apply click noise again for the release action.
	currentPos = h.currentPos // Position might have changed due to hesitate
	releasePos := h.applyClickNoise(currentPos)
	h.currentPos = releasePos

	// Use the internal releaseMouse helper which handles dispatch and state update.
	if err := h.releaseMouse(ctx); err != nil {
		// Error already logged within releaseMouse.
		return err
	}

	// 6. Update fatigue for the clicking action itself.
	h.updateFatigue(0.1)

	return nil
}

// calculateTerminalFittsLaw determines the time required for verification before initiating an action.
// This is primarily used in movement.go for the pause at the end of a move.
// This is an internal helper that assumes the lock is held.
func (h *Humanoid) calculateTerminalFittsLaw(distance float64) time.Duration {
	const W = 20.0 // Assumed target width (W) in pixels for the terminal phase (verification).

	// Index of Difficulty.
	id := math.Log2(1.0 + distance/W)

	// Use dynamic config parameters affected by fatigue.
	A := h.dynamicConfig.FittsA
	B := h.dynamicConfig.FittsB
	rng := h.rng

	// Movement Time (MT) in milliseconds.
	mt := A + B*id

	// Add randomization (+/- 15% jitter).
	mt += mt * (rng.Float64()*0.3 - 0.15)

	if mt < 0 {
		mt = 0
	}

	return time.Duration(mt) * time.Millisecond
}

// calculateClickHoldDuration determines how long the mouse button is held down.
// Assumes the lock is held.
func (h *Humanoid) calculateClickHoldDuration() time.Duration {
	// Use base config for absolute min/max bounds.
	minMs := float64(h.baseConfig.ClickHoldMinMs)
	maxMs := float64(h.baseConfig.ClickHoldMaxMs)
	rng := h.rng

	// Use a distribution skewed towards shorter clicks.
	// Gaussian distribution centered slightly below the midpoint of the range.
	mean := (minMs + maxMs) / 2.0 * 0.9
	stdDev := (maxMs - minMs) / 5.0 // Slightly wider std dev.

	durationMs := mean + rng.NormFloat64()*stdDev

	// Clamp the duration to the configured bounds.
	durationMs = math.Max(minMs, math.Min(maxMs, durationMs))

	// Apply fatigue factor: clicks might become slightly longer when tired.
	durationMs *= (1.0 + h.fatigueLevel*0.25)

	return time.Duration(durationMs) * time.Millisecond
}
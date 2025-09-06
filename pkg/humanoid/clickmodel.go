// -- pkg/humanoid/clickmodel.go --
package humanoid

import (
	"context"
	"math"
	"time"

	// Required for MouseButton constants (e.g., input.MouseButtonLeft)
	"github.com/chromedp/cdproto/input"
	// Required for high-level actions (e.g., chromedp.MouseDown, chromedp.LeftButton)
	"github.com/chromedp/chromedp"
)

// IntelligentClick combines movement, timing, and clicking into a single, chained action.
func (h *Humanoid) IntelligentClick(selector string, field *PotentialField) chromedp.Action {
	if field == nil {
		field = NewPotentialField()
	}

	// We build a sequence of Actions using chromedp.Tasks.
	return chromedp.Tasks{
		// 1. Your custom human-like movement action.
		h.MoveTo(selector, field),

		// 2. Fitts's Law delay before the click.
		chromedp.ActionFunc(func(ctx context.Context) error {
			h.mu.Lock()
			distance := h.lastMovementDistance
			h.mu.Unlock()

			clickDelay := h.calculateTerminalFittsLaw(distance)
			return sleepContext(ctx, clickDelay)
		}),

		// 3. Mouse down using the modern, built-in action.
		// Use SetButtonState wrapper to ensure internal state is tracked for potential low-level interaction later.
		h.SetButtonState(input.MouseButtonLeft, chromedp.MouseDown(chromedp.LeftButton)),

		// 4. Realistic hold duration between down and up.
		chromedp.ActionFunc(func(ctx context.Context) error {
			h.mu.Lock()

			// Ensure the range for Intn is positive to prevent panic.
			rangeMs := h.dynamicConfig.ClickHoldMaxMs - h.dynamicConfig.ClickHoldMinMs
			var randomAddition int
			if rangeMs > 0 {
				randomAddition = h.rng.Intn(rangeMs)
			}

			holdDuration := time.Duration(h.dynamicConfig.ClickHoldMinMs+randomAddition) * time.Millisecond
			h.mu.Unlock()
			return sleepContext(ctx, holdDuration)
		}),

		// 5. Mouse up using the modern, built-in action.
		// Use SetButtonState wrapper to reset internal state.
		h.SetButtonState(input.MouseButtonNone, chromedp.MouseUp(chromedp.LeftButton)),
	}
}

// calculateTerminalFittsLaw determines the time required before initiating a click (terminal latency).
// MT = A + B * log2(1 + D/W).
func (h *Humanoid) calculateTerminalFittsLaw(distance float64) time.Duration {
	const W = 20.0 // Assumed default target width (W) in pixels for the terminal phase.

	// Index of Difficulty (ID)
	id := math.Log2(1.0 + distance/W)

	h.mu.Lock()
	// Use dynamic config parameters (already affected by fatigue).
	A := h.dynamicConfig.FittsA
	B := h.dynamicConfig.FittsB
	rng := h.rng
	h.mu.Unlock()

	// Movement Time (MT) in milliseconds
	mt := A + B*id

	// Add slight randomization (+/- 10%)
	mt += mt * (rng.Float64()*0.2 - 0.1)

	return time.Duration(mt) * time.Millisecond
}
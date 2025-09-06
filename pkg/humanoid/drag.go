//pkg/humanoid/drag.go --
package humanoid

import (
	"context"
	"fmt"

	// Required for MouseButton constants (e.g., input.MouseButtonLeft)
	"github.com/chromedp/cdproto/input"
	// Required for high-level actions (e.g., chromedp.MouseDown, chromedp.LeftButton)
	"github.com/chromedp/chromedp"
	"go.uber.org/zap"
)

// DragAndDrop simulates a human-like drag-and-drop action.
// This function returns a complex Action (chromedp.Tasks).
func (h *Humanoid) DragAndDrop(startSelector, endSelector string) chromedp.Action {
	// We need variables to store the coordinates between steps in the task sequence.
	var start, end Vector2D

	// The entire operation is a sequence of tasks.
	return chromedp.Tasks{
		// 1. Preparation: Locate elements and calculate positions.
		chromedp.ActionFunc(func(ctx context.Context) error {
			// High-intensity action.
			h.updateFatigue(1.5)

			// Locate the start and end elements.
			var err error
			start, err = h.getCenterOfElement(ctx, startSelector)
			if err != nil {
				h.logger.Error("DragAndDrop failed: could not get starting element position",
					zap.String("selector", startSelector),
					zap.Error(err))
				return fmt.Errorf("could not get starting element position: %w", err)
			}

			end, err = h.getCenterOfElement(ctx, endSelector)
			if err != nil {
				h.logger.Error("DragAndDrop failed: could not get ending element position",
					zap.String("selector", endSelector),
					zap.Error(err))
				return fmt.Errorf("could not get ending element position: %w", err)
			}
			return nil
		}),

		// 2. Move to the starting element first.
		h.MoveTo(startSelector, nil),

		// 3. Pause briefly before pressing down.
		chromedp.ActionFunc(func(ctx context.Context) error {
			return h.CognitivePause(ctx, 80, 30)
		}),

		// 4. Mouse down (Grab).
		// Use the SetButtonState wrapper to update the internal tracker for the subsequent low-level drag.
		h.SetButtonState(input.MouseButtonLeft, chromedp.MouseDown(chromedp.LeftButton)),

		// 5. Pause briefly after pressing down before starting the drag.
		chromedp.ActionFunc(func(ctx context.Context) error {
			return h.CognitivePause(ctx, 100, 40)
		}),

		// 6. Execute the drag movement to the end position.
		chromedp.ActionFunc(func(ctx context.Context) error {
			field := NewPotentialField()
			h.mu.Lock()
			attractionStrength := h.dynamicConfig.FittsA
			if attractionStrength <= 0 {
				attractionStrength = 100.0 // Safety fallback
			}
			h.mu.Unlock()

			// The end point is an attractor.
			field.AddSource(end, attractionStrength, 150.0)
			// The start point is a weak repulsor.
			field.AddSource(start, -attractionStrength*0.2, 100.0)

			// Execute the vector-based move. This uses the updated h.currentButtonState.
			return h.MoveToVector(end, field).Do(ctx)
		}),

		// 7. Short pause before release.
		chromedp.ActionFunc(func(ctx context.Context) error {
			return h.CognitivePause(ctx, 70, 30)
		}),

		// 8. Mouse up (Drop).
		// Use the SetButtonState wrapper to reset the internal tracker.
		h.SetButtonState(input.MouseButtonNone, chromedp.MouseUp(chromedp.LeftButton)),
	}
}
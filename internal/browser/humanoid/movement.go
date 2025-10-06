package humanoid

import (
	"context"
	"fmt"
	"math"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	"go.uber.org/zap"
)

// MoveTo is the public, locking method for moving the cursor to a specific element.
func (h *Humanoid) MoveTo(ctx context.Context, selector string, opts *InteractionOptions) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.moveToSelector(ctx, selector, opts)
}

// MoveToVector is the public, locking method for moving to a specific coordinate.
func (h *Humanoid) MoveToVector(ctx context.Context, target Vector2D, opts *InteractionOptions) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.moveToVector(ctx, target, opts)
}

// moveToSelector is the internal, non-locking implementation.
func (h *Humanoid) moveToSelector(ctx context.Context, selector string, opts *InteractionOptions) error {
	// 1. Ensure the target is visible before attempting to measure or move.
	if err := h.ensureVisible(ctx, selector, opts); err != nil {
		// Log the error but continue. If scrolling fails, we might still find the element if it's already in view.
		h.logger.Warn("Humanoid: Failed to ensure element visibility before moving", zap.String("selector", selector), zap.Error(err))
	}

	// 2. Get the element's geometry (after potential scrolling).
	geo, err := h.getElementBoxBySelector(ctx, selector)
	if err != nil {
		return fmt.Errorf("humanoid: failed to locate target '%s' after ensureVisible: %w", selector, err)
	}

	center, valid := boxToCenter(geo)
	if !valid {
		return fmt.Errorf("humanoid: element '%s' has invalid geometry", selector)
	}

	// 3. Determine the initial target point. We estimate zero final velocity for targeting bias calculation.
	target := h.calculateTargetPoint(geo, center, Vector2D{X: 0, Y: 0})

	// 4. Execute the movement.
	return h.moveToVector(ctx, target, opts)
}

// moveToVector is the internal, non-locking core movement logic.
func (h *Humanoid) moveToVector(ctx context.Context, target Vector2D, opts *InteractionOptions) error {
	startPos := h.currentPos
	dist := startPos.Dist(target)

	// No need to move if we're already very close.
	if dist < 1.5 {
		return nil
	}

	// Update fatigue based on the effort (distance).
	h.updateFatigue(dist / 1000.0)

	var field *PotentialField
	if opts != nil {
		field = opts.Field
	}

	// Simulate the trajectory using the Spring-Damped model.
	// The simulation handles its own timing, event dispatching, and updates h.currentPos.
	finalVelocity, err := h.simulateTrajectory(ctx, startPos, target, field, h.currentButtonState)
	if err != nil {
		return err
	}

	// If the movement was significant, simulate the final cognitive pause (Terminal Fitts's Law).
	// This represents the time taken to verify the target before acting.
	if dist > 20.0 {
		terminalPause := h.calculateTerminalFittsLaw(dist)
		// We recover fatigue during this pause.
		h.recoverFatigue(terminalPause)

		// During the terminal pause, the cursor idles slightly (hesitate).
		if err := h.hesitate(ctx, terminalPause); err != nil {
			return err
		}
	}

	h.logger.Debug("moveToVector completed", zap.Any("finalVelocity", finalVelocity), zap.Float64("distance", dist))
	return nil
}

// calculateTargetPoint determines a realistic coordinate within an element's bounds (Target Variability).
// It assumes the caller holds the lock.
func (h *Humanoid) calculateTargetPoint(geo *schemas.ElementGeometry, center Vector2D, estimatedFinalVelocity Vector2D) Vector2D {
	if geo == nil || geo.Width <= 0 || geo.Height <= 0 {
		return center
	}

	width, height := float64(geo.Width), float64(geo.Height)
	rng := h.rng
	clickNoiseStrength := h.dynamicConfig.ClickNoise

	// 1. Determine the primary aim point (Normal distribution near the center).
	// Aim for the inner 80% of the element.
	effectiveWidth := width * 0.8
	effectiveHeight := height * 0.8
	stdDevX := effectiveWidth / 6.0 // 99.7% of points fall within +/- 3 std devs.
	stdDevY := effectiveHeight / 6.0

	offsetX := rng.NormFloat64() * stdDevX
	offsetY := rng.NormFloat64() * stdDevY

	// 2. Apply velocity bias (Overshoot tendency).
	velocityMag := estimatedFinalVelocity.Mag()
	if velocityMag > 500.0 { // Only apply bias if velocity is significant.
		// Normalize the effect of the velocity (0.0 to 1.0).
		normalizedVelocity := math.Min(1.0, velocityMag/maxVelocity)
		// Maximum bias is 10% of the element size.
		maxBiasX := width * 0.1
		maxBiasY := height * 0.1
		velDir := estimatedFinalVelocity.Normalize()

		offsetX += velDir.X * normalizedVelocity * maxBiasX
		offsetY += velDir.Y * normalizedVelocity * maxBiasY
	}

	// 3. Apply random click noise (motor tremor at the point of action).
	offsetX += rng.NormFloat64() * clickNoiseStrength
	offsetY += rng.NormFloat64() * clickNoiseStrength

	finalX := center.X + offsetX
	finalY := center.Y + offsetY

	// 4. Clamp the final point to be strictly within the element's bounds (1-pixel margin).
	minX, maxX := center.X-width/2.0+1.0, center.X+width/2.0-1.0
	minY, maxY := center.Y-height/2.0+1.0, center.Y+height/2.0-1.0

	finalX = math.Max(minX, math.Min(maxX, finalX))
	finalY = math.Max(minY, math.Min(maxY, finalY))

	return Vector2D{X: finalX, Y: finalY}
}
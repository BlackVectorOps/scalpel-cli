//pkg/humanoid/trajectory.go
package humanoid

import (
	"context"
	"math"
	"time"

	// Low-level input package is required here for fine-grained control (NewDispatchMouseEvent).
	// This is the correct import path for the modern API.
	"github.com/chromedp/cdproto/input"
	"go.uber.org/zap"
)

// computeEaseInOutCubic provides a smooth acceleration and deceleration profile.
func computeEaseInOutCubic(t float64) float64 {
	if t < 0.5 {
		return 4 * t * t * t
	}
	return 1 - math.Pow(-2*t+2, 3)/2
}

// calculateFittsLaw determines movement duration based on Fitts's Law.
// This is a simplified version for trajectory timing.
func (h *Humanoid) calculateFittsLaw(distance float64) time.Duration {
	const W = 30.0 // Assumed default target width (W) in pixels.

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

	// Add slight randomization (+/- 15%)
	mt += mt * (rng.Float64()*0.3 - 0.15)

	return time.Duration(mt) * time.Millisecond
}

// generateIdealPath creates a human like trajectory (Bezier curve) deformed by the potential field.
func (h *Humanoid) generateIdealPath(start, end Vector2D, field *PotentialField, numSteps int) []Vector2D {
	p0, p3 := start, end
	mainVec := end.Sub(start)
	dist := mainVec.Mag()

	if dist < 1.0 || numSteps <= 1 {
		return []Vector2D{end}
	}

	mainDir := mainVec.Normalize()

	// Sample forces at 1/3rd and 2/3rds along the path.
	samplePoint1 := start.Add(mainDir.Mul(dist / 3.0))
	force1 := field.CalculateNetForce(samplePoint1)
	samplePoint2 := start.Add(mainDir.Mul(dist * 2.0 / 3.0))
	force2 := field.CalculateNetForce(samplePoint2)

	// Create control points based on the forces.
	p1 := samplePoint1.Add(force1.Mul(dist * 0.1))
	p2 := samplePoint2.Add(force2.Mul(dist * 0.1))

	path := make([]Vector2D, numSteps)
	for i := 0; i < numSteps; i++ {
		t := float64(i) / float64(numSteps-1)
		// Cubic Bezier curve formula.
		omt := 1.0 - t
		omt2 := omt * omt
		omt3 := omt2 * omt
		t2 := t * t
		t3 := t2 * t

		path[i] = p0.Mul(omt3).Add(p1.Mul(3*omt2*t)).Add(p2.Mul(3*omt*t2)).Add(p3.Mul(t3))
	}

	return path
}

// simulateTrajectory moves the mouse along a generated path, dispatching events.
// This function requires low-level control (cdproto/input) for realistic simulation.
func (h *Humanoid) simulateTrajectory(ctx context.Context, start, end Vector2D, field *PotentialField, buttonState input.MouseButton) (Vector2D, error) {
	dist := start.Dist(end)
	h.mu.Lock()
	h.lastMovementDistance = dist
	h.mu.Unlock()

	// Fitts's Law to determine movement duration.
	duration := h.calculateFittsLaw(dist)
	numSteps := int(duration.Seconds() * 100) // ~100 events per second.
	if numSteps < 2 {
		numSteps = 2
	}

	// Ensure the potential field is initialized.
	if field == nil {
		field = NewPotentialField()
	}

	// Generate the ideal path.
	idealPath := h.generateIdealPath(start, end, field, numSteps)

	var velocity Vector2D
	startTime := time.Now()
	lastPos := start
	lastTime := startTime

	for i := 0; i < len(idealPath); i++ {
		t := float64(i) / float64(len(idealPath)-1)
		easedT := computeEaseInOutCubic(t)

		// Calculate the ideal position on the path.
		pathIndex := int(easedT * float64(len(idealPath)-1))
		// Ensure index is safe (float precision might cause issues at the very end).
		if pathIndex >= len(idealPath) {
			pathIndex = len(idealPath) - 1
		}
		currentPos := idealPath[pathIndex]

		// Calculate the target time for this step.
		currentTime := startTime.Add(time.Duration(easedT * float64(duration)))

		// Use context-aware sleep to respect cancellation and adhere to Fitts's law timing.
		if err := sleepContext(ctx, time.Until(currentTime)); err != nil {
			return velocity, err
		}

		// Update velocity based on actual time elapsed.
		now := time.Now()
		dt := now.Sub(lastTime).Seconds()
		if dt > 1e-6 {
			velocity = currentPos.Sub(lastPos).Mul(1.0 / dt)
		}
		lastPos = currentPos
		lastTime = now

		// -- Noise Combination --
		h.mu.Lock()
		perlinMagnitude := h.dynamicConfig.PerlinAmplitude
		h.mu.Unlock()

		perlinFrequency := 0.8

		// 1. Calculate Perlin noise drift.
		timeElapsed := now.Sub(startTime).Seconds()
		perlinDrift := Vector2D{
			X: h.noiseX.Noise1D(timeElapsed*perlinFrequency) * perlinMagnitude,
			Y: h.noiseY.Noise1D(timeElapsed*perlinFrequency) * perlinMagnitude,
		}

		// 2. Add Perlin drift.
		driftAppliedPos := currentPos.Add(perlinDrift)

		// 3. Apply Gaussian noise (tremor).
		finalPerturbedPoint := h.applyGaussianNoise(driftAppliedPos)

		// Dispatch the mouse movement event.
		// Use the modern constructor pattern `input.NewDispatchMouseEvent`.
		dispatchMouse := input.NewDispatchMouseEvent(input.EventMouseMoved, finalPerturbedPoint.X, finalPerturbedPoint.Y)

		// Include button state if dragging.
		if buttonState != input.MouseButtonNone {
			dispatchMouse = dispatchMouse.WithButton(buttonState)

			// CRITICAL: When dragging (MouseMoved with button pressed), the 'buttons' field (plural, bitmask)
			// must also be set according to the CDP specification.
			var buttons int64
			switch buttonState {
			case input.MouseButtonLeft:
				buttons = 1
			case input.MouseButtonRight:
				buttons = 2
			case input.MouseButtonMiddle:
				buttons = 4
			}
			if buttons > 0 {
				dispatchMouse = dispatchMouse.WithButtons(buttons)
			}
		}

		// Execute the low-level command.
		if err := dispatchMouse.Do(ctx); err != nil {
			h.logger.Warn("Humanoid: Failed to dispatch mouse move event during simulation", zap.Error(err))
			return velocity, err
		}

		// Update the internal position tracker.
		h.mu.Lock()
		h.currentPos = finalPerturbedPoint
		h.mu.Unlock()

		// Simulate browser rendering/event loop delay.
		h.mu.Lock()
		// Ensure Intn argument is positive
		randPart := 0
		if 4 > 0 {
			randPart = h.rng.Intn(4)
		}
		sleepDuration := time.Duration(2+randPart) * time.Millisecond
		h.mu.Unlock()

		// Use context-aware sleep.
		if err := sleepContext(ctx, sleepDuration); err != nil {
			return velocity, err
		}
	}

	return velocity, nil
}
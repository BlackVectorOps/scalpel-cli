package humanoid

import (
	"context"
	"math"
	"time"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	"go.uber.org/zap"
)

// Constants for the simulation loop.
const (
	// timeStep defines the granularity of the physics simulation (e.g., 5ms for 200Hz).
	timeStep = 5 * time.Millisecond
	// maxSimulationTime prevents infinite loops if the target is never reached.
	maxSimulationTime = 10 * time.Second
)

// calculateFittsLawInternal is deprecated for trajectory generation but used for time estimation in some contexts.
func (h *Humanoid) calculateFittsLawInternal(distance float64) time.Duration {
	const W = 30.0 // Assumed default target width (W) in pixels.
	id := math.Log2(1.0 + distance/W)

	A := h.dynamicConfig.FittsA
	B := h.dynamicConfig.FittsB
	rng := h.rng

	mt := A + B*id
	mt += mt * (rng.Float64()*0.3 - 0.15) // +/- 15%

	return time.Duration(mt) * time.Millisecond
}

// simulateTrajectory simulates mouse movement using a spring-damped system influenced by potential fields.
// This approach generates realistic velocity profiles and micro-corrections naturally.
// It assumes the caller holds the lock.
func (h *Humanoid) simulateTrajectory(ctx context.Context, start, end Vector2D, field *PotentialField, buttonState schemas.MouseButton) (Vector2D, error) {
	// Initialize simulation state.
	currentPos := start
	velocity := Vector2D{X: 0, Y: 0} // Start with zero velocity.
	t := time.Duration(0)

	// Use dynamic configuration parameters (affected by fatigue).
	omega := h.dynamicConfig.Omega // Natural frequency (speed)
	zeta := h.dynamicConfig.Zeta   // Damping ratio (smoothness/oscillation)

	if field == nil {
		field = NewPotentialField()
	}

	buttonsBitfield := h.calculateButtonsBitfield(buttonState)
	rng := h.rng
	perlinMagnitude := h.dynamicConfig.PerlinAmplitude
	perlinFrequency := 0.8

	// Variables for Optimized Submovement Model.
	currentTarget := end
	isMicroCorrection := false
	initialDist := start.Dist(end)

	startTime := time.Now()

	// --- Simulation Loop ---
	for t < maxSimulationTime {
		if ctx.Err() != nil {
			return velocity, ctx.Err()
		}

		// 1. Check termination condition.
		distanceToTarget := currentPos.Dist(currentTarget)
		currentVelocityMag := velocity.Mag()

		// Stop if we are very close to the current target AND moving slowly.
		if distanceToTarget < 1.0 && currentVelocityMag < 50.0 {
			// If we reached the final destination, we are done.
			if currentTarget == end {
				break
			}
			// If we reached a submovement target, switch focus to the final target.
			currentTarget = end
			isMicroCorrection = false
			continue // Re-evaluate forces immediately for the new target.
		}

		// 2. Handle Micro-corrections/Submovements (Optimized Submovement Model).
		// Only consider initiating a submovement if the initial distance was significant.
		if !isMicroCorrection && initialDist > h.dynamicConfig.MicroCorrectionThreshold {
			// Time-to-contact estimation (TTC).
			ttc := distanceToTarget / math.Max(1.0, currentVelocityMag)

			// If TTC is low (arriving soon), but we are still somewhat far, initiate a correction.
			if ttc < 0.1 && distanceToTarget > 15.0 && rng.Float64() < 0.3 {
				isMicroCorrection = true
				// Define a new sub-target. This simulates the brain adjusting the trajectory mid-flight.
				adjustmentFactor := 0.8 + rng.Float64()*0.4 // Jitter the adjustment
				correctionVector := end.Sub(currentPos).Mul(adjustmentFactor)
				currentTarget = currentPos.Add(correctionVector)
				h.logger.Debug("Humanoid: Initiating micro-correction",
					zap.Float64("distance", distanceToTarget),
					zap.Float64("ttc", ttc),
					zap.Float64("velocity", currentVelocityMag))
			}
		}

		// 3. Calculate Forces (F = ma).
		// Spring force towards the current target (Hooke's Law: F = kx).
		displacement := currentTarget.Sub(currentPos)
		// k = omega^2 (assuming mass m=1)
		springForce := displacement.Mul(omega * omega)

		// Damping force opposing velocity (F = cv).
		// c = 2*zeta*omega (assuming mass m=1)
		dampingForce := velocity.Mul(-2.0 * zeta * omega)

		// External forces from the potential field (e.g., avoiding obstacles).
		externalForce := field.CalculateNetForce(currentPos)

		// Net acceleration (a = F/m).
		acceleration := springForce.Add(dampingForce).Add(externalForce)

		// 4. Update Velocity and Position (Semi-implicit Euler integration).
		dt := timeStep.Seconds()
		velocity = velocity.Add(acceleration.Mul(dt))

		// Clamp velocity to realistic maximums (defined in humanoid.go).
		if velocity.Mag() > maxVelocity {
			velocity = velocity.Normalize().Mul(maxVelocity)
		}

		currentPos = currentPos.Add(velocity.Mul(dt))

		// 5. Apply Noise and Perturbations.
		// Apply Perlin noise (low-frequency drift/waver).
		timeElapsed := time.Now().Sub(startTime).Seconds()
		perlinDrift := Vector2D{
			X: h.noiseX.Noise1D(timeElapsed*perlinFrequency) * perlinMagnitude,
			Y: h.noiseY.Noise1D(timeElapsed*perlinFrequency) * perlinMagnitude,
		}
		driftAppliedPos := currentPos.Add(perlinDrift)

		// Apply Gaussian noise (high-frequency tremor).
		finalPerturbedPoint := h.applyGaussianNoise(driftAppliedPos)

		// 6. Dispatch Event.
		eventData := schemas.MouseEventData{
			Type:    schemas.MouseMove,
			X:       finalPerturbedPoint.X,
			Y:       finalPerturbedPoint.Y,
			Button:  schemas.ButtonNone,
			Buttons: buttonsBitfield,
		}

		if err := h.executor.DispatchMouseEvent(ctx, eventData); err != nil {
			if ctx.Err() == nil {
				h.logger.Warn("Humanoid: Failed to dispatch mouse move event", zap.Error(err))
			}
			return velocity, err
		}

		// 7. Update State and Timing.
		h.currentPos = finalPerturbedPoint
		t += timeStep

		// Sleep for the duration of the time step to maintain real-time simulation speed.
		// The sleep duration is slightly jittered to avoid perfect periodicity.
		sleepDuration := timeStep + time.Duration(rng.Intn(3)-1)*time.Millisecond
		if sleepDuration > 0 {
			if err := h.executor.Sleep(ctx, sleepDuration); err != nil {
				return velocity, err
			}
		}
	}

	if t >= maxSimulationTime {
		h.logger.Warn("Humanoid: Movement simulation timed out", zap.Any("start", start), zap.Any("end", end))
	}

	// Return the final velocity achieved at the end of the movement.
	return velocity, nil
}
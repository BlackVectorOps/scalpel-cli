package browser

import (
	"context"
	"math/rand"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
	"go.uber.org/zap"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	"github.com/xkilldash9x/scalpel-cli/internal/humanoid"
)

// StabilizationFunc is a function type that waits for the application state to stabilize.
type StabilizationFunc func(ctx context.Context) error

// valueOnlyContext is a context that inherits values but not cancellation.
// This is crucial for cleanup tasks that must run even if the parent context is cancelled.
type valueOnlyContext struct{ context.Context }

func (valueOnlyContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (valueOnlyContext) Done() <-chan struct{}       { return nil }
func (valueOnlyContext) Err() error                  { return nil }

// Interactor is responsible for intelligently interacting with web pages.
type Interactor struct {
	logger      *zap.Logger
	humanoid    *humanoid.Humanoid
	stabilizeFn StabilizationFunc
	rng         *rand.Rand
}

// NewInteractor creates a new interactor instance.
func NewInteractor(logger *zap.Logger, h *humanoid.Humanoid, stabilizeFn StabilizationFunc) *Interactor {
	source := rand.NewSource(time.Now().UnixNano())
	if stabilizeFn == nil {
		stabilizeFn = func(ctx context.Context) error { return nil }
	}
	return &Interactor{
		logger:      logger.Named("interactor"),
		humanoid:    h,
		stabilizeFn: stabilizeFn,
		rng:         rand.New(source),
	}
}

// RecursiveInteract is the main entry point for the interaction logic.
func (i *Interactor) RecursiveInteract(ctx context.Context, config schemas.InteractionConfig) error {
	if _, ok := ctx.Deadline(); !ok {
		i.logger.Warn("RecursiveInteract called without a timeout context. This risks stalling the worker.")
	}
	interactedElements := make(map[string]bool)
	i.logger.Info("Starting recursive interaction.", zap.Int("max_depth", config.MaxDepth))

	if err := i.humanoid.CognitivePause(800, 300).Do(ctx); err != nil {
		return err
	}
	return i.interactDepth(ctx, config, 0, interactedElements)
}

// interactDepth handles the interaction logic for a specific depth.
func (i *Interactor) interactDepth(
	ctx context.Context,
	config schemas.InteractionConfig,
	depth int,
	interactedElements map[string]bool,
) error {
	if depth >= config.MaxDepth || ctx.Err() != nil {
		return ctx.Err()
	}

	log := i.logger.With(zap.Int("depth", depth))

	// 1. Discover new elements on the page (delegated to logic in interactor_discovery.go).
	newElements, err := i.discoverElements(ctx, interactedElements)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Warn("Failed to query for interactive elements.", zap.Error(err))
		return nil
	}
	if len(newElements) == 0 {
		return nil
	}

	// 2. Shuffle for randomness.
	i.rng.Shuffle(len(newElements), func(j, k int) {
		newElements[j], newElements[k] = newElements[k], newElements[j]
	})

	// 3. Execute interactions on a subset of discovered elements.
	interactions := 0
	for _, element := range newElements {
		if interactions >= config.MaxInteractionsPerDepth || ctx.Err() != nil {
			break
		}

		if err := i.humanoid.CognitivePause(150, 70).Do(ctx); err != nil {
			return err
		}

		// Execute the interaction (delegated to logic in interactor_execution.go).
		actionCtx, cancelAction := context.WithTimeout(ctx, 20*time.Second)
		success, err := i.executeInteraction(actionCtx, element, log)
		cancelAction()

		interactedElements[element.Fingerprint] = true

		if err != nil {
			log.Debug("Interaction failed.", zap.String("desc", element.Description), zap.Error(err))
			continue
		}
		if success {
			interactions++
			delay := time.Duration(config.InteractionDelayMs) * time.Millisecond
			if delay > 0 {
				if err := i.humanoid.Hesitate(delay).Do(ctx); err != nil {
					return err
				}
			}
		}
	}

	// 4. Recurse if the state changed.
	if interactions > 0 {
		log.Debug("Interactions occurred. Waiting for stabilization before recursing.", zap.Int("interactions", interactions))
		if err := i.stabilizeFn(ctx); err != nil && ctx.Err() == nil {
			log.Warn("Stabilization failed after interaction.", zap.Error(err))
		}
		waitDuration := time.Duration(config.PostInteractionWaitMs) * time.Millisecond
		if waitDuration > 0 {
			if err := i.humanoid.Hesitate(waitDuration).Do(ctx); err != nil {
				return err
			}
		}
		return i.interactDepth(ctx, config, depth+1, interactedElements)
	}
	return nil
}

// discoverElements is a helper method to encapsulate the discovery logic.
func (i *Interactor) discoverElements(ctx context.Context, interacted map[string]bool) ([]interactiveElement, error) {
	clickableSelectors := "a[href], button, [onclick], [role=button], [role=link], input[type=submit], input[type=button], input[type=reset], summary, details"
	inputSelectors := "input:not([type=hidden]):not([type=submit]):not([type=button]):not([type=reset]), textarea, select"

	var clickableNodes, inputNodes []*cdp.Node

	queryCtx, cancelQuery := context.WithTimeout(ctx, 15*time.Second)
	defer cancelQuery()

	err := chromedp.Run(queryCtx,
		chromedp.Nodes(clickableSelectors, &clickableNodes, chromedp.ByQueryAll, chromedp.NodeVisible),
		chromedp.Nodes(inputSelectors, &inputNodes, chromedp.ByQueryAll, chromedp.NodeVisible),
	)
	if err != nil {
		return nil, err
	}

	// Call the fingerprinting logic which now lives in interactor_discovery.go
	newElements := i.filterAndFingerprint(inputNodes, interacted, true)
	newElements = append(newElements, i.filterAndFingerprint(clickableNodes, interacted, false)...)
	return newElements, nil
}

package browser

import (
	"context"
	"fmt"
	"hash"
	"hash/fnv"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
	"go.uber.org/zap"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	"github.com/xkilldash9x/scalpel-cli/internal/humanoid"
)

// StabilizationFunc is a function type that waits for the application state to stabilize (Principle 2).
type StabilizationFunc func(ctx context.Context) error

// interactiveElement is a helper struct to store a node and its pre-calculated fingerprint.
type interactiveElement struct {
	Node        *cdp.Node
	Fingerprint string
	Description string
	IsInput     bool // Flag to distinguish inputs from clickable elements
}

// hasherPool reuses FNV hasher instances to reduce memory allocations during fingerprinting.
var hasherPool = sync.Pool{
	New: func() interface{} {
		// FNV-1a is fast and sufficient for non-cryptographic hashing.
		return fnv.New64a()
	},
}

// valueOnlyContext is a context that inherits values from its parent but not cancellation.
// This is crucial for cleanup tasks that must run even if the parent context is cancelled (Principle 4).
type valueOnlyContext struct{ context.Context }

func (valueOnlyContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (valueOnlyContext) Done() <-chan struct{}       { return nil }
func (valueOnlyContext) Err() error                  { return nil }

// Interactor is responsible for intelligently interacting with web pages.
type Interactor struct {
	logger      *zap.Logger
	humanoid    *humanoid.Humanoid
	stabilizeFn StabilizationFunc // Function used for dynamic waits (Principle 2).
	rng         *rand.Rand        // A dedicated RNG for interaction randomization.
}

// NewInteractor creates a new interactor instance.
func NewInteractor(logger *zap.Logger, h *humanoid.Humanoid, stabilizeFn StabilizationFunc) *Interactor {
	// Seed a dedicated random number generator.
	source := rand.NewSource(time.Now().UnixNano())

	// Use a default NOP stabilizer if none is provided.
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

// RecursiveInteract is the main entry point for the interaction logic (DFS strategy).
func (i *Interactor) RecursiveInteract(ctx context.Context, config schemas.InteractionConfig) error {
	// Principle 3: The provided ctx should have a timeout.
	if _, ok := ctx.Deadline(); !ok {
		i.logger.Warn("RecursiveInteract called without a timeout context. This risks stalling the worker.")
	}

	interactedElements := make(map[string]bool)
	i.logger.Info("Starting recursive interaction.", zap.Int("max_depth", config.MaxDepth))

	// Initial pause.
	if err := i.humanoid.CognitivePause(800, 300).Do(ctx); err != nil {
		return err
	}

	return i.interactDepth(ctx, config, 0, interactedElements)
}

// interactDepth handles the interaction logic for a specific depth in the DFS.
func (i *Interactor) interactDepth(
	ctx context.Context,
	config schemas.InteractionConfig,
	depth int,
	interactedElements map[string]bool,
) error {
	// -- Check for exit conditions --
	if depth >= config.MaxDepth {
		i.logger.Debug("Reached max interaction depth.", zap.Int("depth", depth))
		return nil
	}
	// Principle 3: Always check context cancellation.
	if ctx.Err() != nil {
		return ctx.Err()
	}

	log := i.logger.With(zap.Int("depth", depth))

	// 1. Identify Interactive Elements
	clickableSelectors := "a[href], button, [onclick], [role=button], [role=link], input[type=submit], input[type=button], input[type=reset], summary, details"
	inputSelectors := "input:not([type=hidden]):not([type=submit]):not([type=button]):not([type=reset]), textarea, select"

	var clickableNodes, inputNodes []*cdp.Node

	// Principle 3: Timeout for querying elements.
	queryCtx, cancelQuery := context.WithTimeout(ctx, 15*time.Second)
	defer cancelQuery()

	// Query the DOM for visible nodes.
	err := chromedp.Run(queryCtx,
		chromedp.Nodes(clickableSelectors, &clickableNodes, chromedp.ByQueryAll, chromedp.NodeVisible),
		chromedp.Nodes(inputSelectors, &inputNodes, chromedp.ByQueryAll, chromedp.NodeVisible),
	)

	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Warn("Failed to query for interactive elements. Stopping exploration at this depth.", zap.Error(err))
		return nil
	}

	// 2. Filter and Fingerprint
	newElements := i.filterAndFingerprint(inputNodes, interactedElements, true)
	newElements = append(newElements, i.filterAndFingerprint(clickableNodes, interactedElements, false)...)

	if len(newElements) == 0 {
		return nil
	}

	// 3. Shuffle
	i.rng.Shuffle(len(newElements), func(j, k int) {
		newElements[j], newElements[k] = newElements[k], newElements[j]
	})

	// 4. Execute Interactions
	interactions := 0
	for _, element := range newElements {
		if interactions >= config.MaxInteractionsPerDepth {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if err := i.humanoid.CognitivePause(150, 70).Do(ctx); err != nil {
			return err
		}

		// Principle 3: Timeout for individual actions.
		actionCtx, cancelAction := context.WithTimeout(ctx, 20*time.Second)
		success, err := i.executeInteraction(actionCtx, element, log)
		cancelAction()

		interactedElements[element.Fingerprint] = true

		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Debug("Interaction failed.", zap.String("desc", element.Description), zap.Error(err))
			continue
		}

		if success {
			interactions++
			// Delay between actions.
			delay := time.Duration(config.InteractionDelayMs) * time.Millisecond
			if delay > 0 {
				if err := i.humanoid.Hesitate(delay).Do(ctx); err != nil {
					return err
				}
			}
		}
	}

	// 5. Recurse if State Changed
	if interactions > 0 {
		log.Debug("Interactions occurred. Waiting for stabilization before recursing.", zap.Int("interactions", interactions))

		// Principle 2: Use the dynamic stabilization function (e.g., WaitNetworkIdle).
		if err := i.stabilizeFn(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Stabilization timed out, but we proceed anyway, logging a warning.
			log.Warn("Stabilization (network idle) failed after interaction. Proceeding, but results may be inconsistent.", zap.Error(err))
		}

		// Apply optional configured static wait *after* dynamic stabilization, if specified.
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

// executeInteraction performs the action using a robust tagging strategy.
func (i *Interactor) executeInteraction(ctx context.Context, element interactiveElement, log *zap.Logger) (bool, error) {
	// Robust interaction strategy: tag the specific node with a unique ID.
	tempID := fmt.Sprintf("scalpel-interaction-%d-%d", time.Now().UnixNano(), i.rng.Int63())
	attributeName := "data-scalpel-id"
	selector := fmt.Sprintf(`[%s="%s"]`, attributeName, tempID)

	// Set the temporary attribute using the node's unique BackendNodeID (via chromedp.ByID).
	err := chromedp.Run(ctx,
		chromedp.SetAttributeValue(element.Node.NodeID, attributeName, tempID, chromedp.ByID),
	)
	if err != nil {
		// This often happens if the element became stale between querying and interaction.
		return false, fmt.Errorf("failed to tag element for interaction (might be stale): %w", err)
	}

	// Always clean up the attribute, even if the interaction fails (Principle 4).
	defer i.cleanupInteractionAttribute(ctx, selector, attributeName, log)

	// -- Determine the interaction type --
	var interactionAction chromedp.Action
	nodeName := strings.ToUpper(element.Node.NodeName)

	if element.IsInput {
		if nodeName == "SELECT" {
			interactionAction = i.handleSelectInteraction(selector, element.Node)
		} else {
			payload := i.generateInputPayload(element.Node)
			interactionAction = i.humanoid.Type(selector, payload)
		}
	} else {
		// Use humanoid movement and clicking behavior.
		interactionAction = i.humanoid.IntelligentClick(selector, nil)
	}

	if interactionAction == nil {
		return false, fmt.Errorf("no viable interaction action for element")
	}

	// Perform the action.
	if err = chromedp.Run(ctx, interactionAction); err != nil {
		return false, fmt.Errorf("humanoid action failed: %w", err)
	}

	return true, nil
}

// (Helper methods: filterAndFingerprint, handleSelectInteraction, generateInputPayload are included below)

// cleanupInteractionAttribute removes the temporary attribute using JavaScript evaluation.
func (i *Interactor) cleanupInteractionAttribute(ctx context.Context, selector, attributeName string, log *zap.Logger) {
	if chromedp.FromContext(ctx) == nil {
		log.Debug("Could not get valid chromedp context for cleanup.")
		return
	}

	// Create a detached context (valueOnlyContext) that ignores cancellation from the parent task (Principle 4).
	detachedCtx := valueOnlyContext{ctx}
	// Principle 3: Apply a strict timeout to the cleanup operation.
	taskCtx, cancelTask := context.WithTimeout(detachedCtx, 2*time.Second)
	defer cancelTask()

	// JavaScript to remove the attribute.
	jsCleanup := fmt.Sprintf(`
        (function() {
            const el = document.querySelector('%s');
            if (el) {
                el.removeAttribute('%s');
                return true;
            }
            return false;
        })()`, selector, attributeName)

	var res bool
	err := chromedp.Run(taskCtx, chromedp.Evaluate(jsCleanup, &res))

	if err != nil && taskCtx.Err() == nil {
		log.Debug("Failed to execute cleanup JS.", zap.String("selector", selector), zap.Error(err))
	}
}

// (Utility Functions: attributeMap, isDisabled, generateNodeFingerprint, and remaining Helper methods included below)

// filterAndFingerprint identifies new elements and creates stable fingerprints for them.
func (i *Interactor) filterAndFingerprint(nodes []*cdp.Node, interacted map[string]bool, isInput bool) []interactiveElement {
	newElements := make([]interactiveElement, 0, len(nodes))

	for _, node := range nodes {
		attrs := attributeMap(node)
		// Ignore disabled or readonly elements.
		if isDisabled(node, attrs) {
			continue
		}

		fingerprint, description := generateNodeFingerprint(node, attrs)
		if fingerprint == "" {
			continue
		}

		if !interacted[fingerprint] {
			newElements = append(newElements, interactiveElement{
				Node:        node,
				Fingerprint: fingerprint,
				Description: description,
				IsInput:     isInput,
			})
		}
	}
	return newElements
}

// handleSelectInteraction picks a random valid option from a dropdown.
func (i *Interactor) handleSelectInteraction(selector string, node *cdp.Node) chromedp.Action {
	var options []string
	for _, child := range node.Children {
		if strings.ToUpper(child.NodeName) == "OPTION" {
			childAttrs := attributeMap(child)
			if value, ok := childAttrs["value"]; ok && value != "" {
				if _, disabled := childAttrs["disabled"]; !disabled {
					options = append(options, value)
				}
			}
		}
	}

	if len(options) == 0 {
		return nil // No options to choose from.
	}

	// Pick one at random.
	selectedValue := options[i.rng.Intn(len(options))]

	// A user clicks to open, pauses, then selects. We do the same.
	return chromedp.Tasks{
		i.humanoid.IntelligentClick(selector, nil),
		i.humanoid.CognitivePause(150, 50),
		chromedp.SetValue(selector, selectedValue, chromedp.ByQuery),
	}
}

// generateInputPayload creates context-aware test data for input fields.
func (i *Interactor) generateInputPayload(node *cdp.Node) string {
	attrs := attributeMap(node)
	inputType, _ := attrs["type"]
	inputName, _ := attrs["name"]
	inputId, _ := attrs["id"]
	contextString := strings.ToLower(inputType) + " " + strings.ToLower(inputName) + " " + strings.ToLower(inputId)

	if inputType == "email" || strings.Contains(contextString, "email") {
		return "test.user@example.com"
	}
	if inputType == "password" || strings.Contains(contextString, "pass") {
		return "ScalpelTest123!"
	}
	if inputType == "tel" || strings.Contains(contextString, "phone") {
		return "555-0199"
	}
	if inputType == "search" || strings.Contains(contextString, "query") {
		return "test query"
	}
	if strings.Contains(contextString, "name") || strings.Contains(contextString, "user") {
		return "Test User"
	}

	// When in doubt, use a generic string.
	return "scalpel test input"
}

// attributeMap converts a node's attribute slice into a more convenient map.
func attributeMap(node *cdp.Node) map[string]string {
	attrs := make(map[string]string)
	if len(node.Attributes) > 0 {
		for i := 0; i < len(node.Attributes); i += 2 {
			if i+1 < len(node.Attributes) {
				attrs[node.Attributes[i]] = node.Attributes[i+1]
			}
		}
	}
	return attrs
}

// isDisabled checks if a node is disabled or readonly.
func isDisabled(node *cdp.Node, attrs map[string]string) bool {
	if _, ok := attrs["disabled"]; ok {
		return true
	}
	nodeName := strings.ToUpper(node.NodeName)
	if nodeName == "INPUT" || nodeName == "TEXTAREA" {
		if _, ok := attrs["readonly"]; ok {
			return true
		}
	}
	return false
}

// generateNodeFingerprint creates a stable identifier for a DOM node.
func generateNodeFingerprint(node *cdp.Node, attrs map[string]string) (string, string) {
	var sb strings.Builder
	sb.WriteString(strings.ToLower(node.NodeName))

	// Use stable, identifying attributes first.
	if id, ok := attrs["id"]; ok && id != "" {
		sb.WriteString("#" + id)
	}
	if cls, ok := attrs["class"]; ok && cls != "" {
		classes := strings.Fields(cls)
		sort.Strings(classes) // Sort for consistency.
		sb.WriteString("." + strings.Join(classes, "."))
	}

	// Add other relevant attributes.
	attributesToInclude := []string{"name", "href", "type", "role", "aria-label", "placeholder", "title"}
	sort.Strings(attributesToInclude) // Sort for consistency.
	for _, attr := range attributesToInclude {
		if val, ok := attrs[attr]; ok && val != "" {
			sb.WriteString(fmt.Sprintf(`[%s="%s"]`, attr, val))
		}
	}

	description := sb.String()
	if description == "" {
		return "", ""
	}

	// Hash the descriptive string for a compact fingerprint.
	hasher := hasherPool.Get().(hash.Hash64)
	defer func() {
		hasher.Reset()
		hasherPool.Put(hasher)
	}()

	_, _ = hasher.Write([]byte(description))
	hashVal := strconv.FormatUint(hasher.Sum64(), 16)

	return hashVal, description
}
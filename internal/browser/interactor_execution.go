package browser

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
	"go.uber.org/zap"
)

// executeInteraction performs the action using a robust tagging strategy.
func (i *Interactor) executeInteraction(ctx context.Context, element interactiveElement, log *zap.Logger) (bool, error) {
	tempID := fmt.Sprintf("scalpel-interaction-%d-%d", time.Now().UnixNano(), i.rng.Int63())
	attributeName := "data-scalpel-id"
	selector := fmt.Sprintf(`[%s="%s"]`, attributeName, tempID)

	// Use the modern, non-deprecated call. The 'chromedp.ByID' option is not needed
	// when providing a cdp.NodeID directly.
	err := chromedp.Run(ctx,
		chromedp.SetAttributeValue(element.Node.NodeID, attributeName, tempID),
	)
	if err != nil {
		return false, fmt.Errorf("failed to tag element for interaction (might be stale): %w", err)
	}
	defer i.cleanupInteractionAttribute(ctx, selector, attributeName, log)

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
		interactionAction = i.humanoid.IntelligentClick(selector, nil)
	}

	if interactionAction == nil {
		return false, fmt.Errorf("no viable interaction action for element")
	}

	if err = chromedp.Run(ctx, interactionAction); err != nil {
		return false, fmt.Errorf("humanoid action failed: %w", err)
	}
	return true, nil
}

// cleanupInteractionAttribute removes the temporary attribute.
func (i *Interactor) cleanupInteractionAttribute(ctx context.Context, selector, attributeName string, log *zap.Logger) {
	if chromedp.FromContext(ctx) == nil {
		log.Debug("Could not get valid chromedp context for cleanup.")
		return
	}

	detachedCtx := valueOnlyContext{ctx}
	taskCtx, cancelTask := context.WithTimeout(detachedCtx, 2*time.Second)
	defer cancelTask()

	jsCleanup := fmt.Sprintf(`document.querySelector('%s')?.removeAttribute('%s')`, selector, attributeName)
	err := chromedp.Run(taskCtx, chromedp.Evaluate(jsCleanup, nil))
	if err != nil && taskCtx.Err() == nil {
		log.Debug("Failed to execute cleanup JS.", zap.String("selector", selector), zap.Error(err))
	}
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
		return nil
	}
	selectedValue := options[i.rng.Intn(len(options))]
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
	contextString := strings.ToLower(inputType + " " + inputName + " " + inputId)

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
	return "scalpel test input"
}

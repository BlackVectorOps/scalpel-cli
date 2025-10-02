// Package shadowdom provides utilities for processing Declarative Shadow DOM (DSD)
// within an HTML document. It handles detecting shadow hosts, instantiating shadow
// trees from templates, extracting encapsulated styles, and assigning slotted content.
package shadowdom

import (
	"strings"

	"github.com/xkilldash9x/scalpel-cli/internal/browser/parser"
	"github.com/xkilldash9x/scalpel-cli/internal/browser/style"
	"golang.org/x/net/html"
)

// -- Structs --

// Engine can be used to hold state or configuration for DOM processing.
// For this consolidated module, it serves as a receiver anchor for the methods.
type Engine struct{}

var _ style.ShadowDOMProcessor = (*Engine)(nil)

// -- Core Shadow DOM Logic --

// DetectShadowHost checks if a node is a shadow host by looking for
// a direct child <template> with a 'shadowrootmode' attribute.
func (e *Engine) DetectShadowHost(node *html.Node) bool {
	// A valid host must be an element node.
	if node == nil || node.Type != html.ElementNode {
		return false
	}

	for c := node.FirstChild; c != nil; c = c.NextSibling {
		// The <template> tag is an ElementNode.
		if c.Type == html.ElementNode && c.Data == "template" {
			// If the template has the magic attribute, we've found a host.
			if getAttr(c, "shadowrootmode") != "" {
				return true
			}
		}
	}

	return false
}

// InstantiateShadowRoot finds the DSD template, clones its content to create a
// shadow tree, and extracts any encapsulated stylesheets. This process emulates
// how a browser would instantiate a shadow DOM from a template.
func (e *Engine) InstantiateShadowRoot(host *html.Node) (*html.Node, []parser.StyleSheet) {
	// First, find the DSD template element itself.
	var templateNode *html.Node
	for child := host.FirstChild; child != nil; child = child.NextSibling {
		// The <template> tag is an ElementNode.
		if child.Type == html.ElementNode && child.Data == "template" {
			if getAttr(child, "shadowrootmode") != "" {
				templateNode = child
				break
			}
		}
	}

	if templateNode == nil {
		// No DSD template found, so no shadow root to instantiate.
		return nil, nil
	}

	// The content of the template needs to be cloned to create the shadow tree.
	// The parser often wraps template content in a DocumentFragment.
	contentSource := templateNode
	// FIX #2: The correct constant is DocumentNode.
	if templateNode.FirstChild != nil && templateNode.FirstChild.Type == html.DocumentNode {
		contentSource = templateNode.FirstChild
	}

	// Create a synthetic root to act as the boundary for the new shadow tree.
	// FIX #2: The correct constant is DocumentNode.
	shadowRoot := &html.Node{
		Type: html.DocumentNode,
		Data: "shadow-root-boundary", // A marker for easier debugging.
	}

	// Clone all children from the template's content into our new shadow root.
	for child := contentSource.FirstChild; child != nil; child = child.NextSibling {
		shadowRoot.AppendChild(cloneNode(child))
	}

	// Now, traverse the *newly created* shadow tree to find and process styles.
	var stylesheets []parser.StyleSheet
	var nodesToRemove []*html.Node

	var findAndParseStyles func(*html.Node)
	findAndParseStyles = func(n *html.Node) {
		// The <style> tag is an ElementNode.
		if n.Type == html.ElementNode && n.Data == "style" {
			// We've found a style tag, let's process it.
			if n.FirstChild != nil && n.FirstChild.Type == html.TextNode {
				cssContent := n.FirstChild.Data
				p := parser.NewParser(cssContent)
				// Corrected assignment mismatch. The compiler says p.Parse() returns only one value.
				parsedSheet := p.Parse()
				stylesheets = append(stylesheets, parsedSheet)
			}
			// Mark this <style> node for removal from the shadow tree
			// so it's not part of the final rendered structure.
			nodesToRemove = append(nodesToRemove, n)
			return // Don't traverse into children of a <style> tag.
		}

		// Don't traverse into nested templates. They are inert until their
		// own host is processed.
		// The <template> tag is an ElementNode.
		if n.Type == html.ElementNode && n.Data == "template" {
			return
		}

		// Continue the traversal.
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			findAndParseStyles(c)
		}
	}

	findAndParseStyles(shadowRoot)

	// Clean up by removing the processed <style> nodes from the cloned tree.
	for _, node := range nodesToRemove {
		if node.Parent != nil {
			node.Parent.RemoveChild(node)
		}
	}

	return shadowRoot, stylesheets
}

// AssignSlots distributes the light DOM children of a shadow host into the
// <slot> elements defined within its shadow DOM.
func (e *Engine) AssignSlots(host *style.StyledNode) {
	if host.ShadowRoot == nil {
		return // Nothing to do if there's no shadow root.
	}

	// Step 1: Categorize all of the host's direct children (the "light DOM")
	// into named slots or the default slot.
	namedSlottables := make(map[string][]*style.StyledNode)
	var defaultSlottables []*style.StyledNode

	for _, child := range host.Children {
		isSlottable := false
		slotName := ""

		// A slottable node must be an ElementNode.
		if child.Node.Type == html.ElementNode {
			isSlottable = true
			slotName = getAttr(child.Node, "slot")
		} else if child.Node.Type == html.TextNode && strings.TrimSpace(child.Node.Data) != "" {
			// Non empty text nodes are also slottable.
			isSlottable = true
		}

		if isSlottable {
			if slotName != "" {
				namedSlottables[slotName] = append(namedSlottables[slotName], child)
			} else {
				defaultSlottables = append(defaultSlottables, child)
			}
		}
	}

	// Step 2: Traverse the shadow tree to find <slot> elements and
	// assign the categorized nodes to them.
	var traverseShadow func(*style.StyledNode)
	traverseShadow = func(node *style.StyledNode) {
		if node == nil {
			return
		}

		// If we encounter another shadow host inside this shadow tree, stop.
		// Slot assignment doesn't cross shadow boundaries.
		if node != host.ShadowRoot && node.ShadowRoot != nil {
			return
		}

		// The <slot> tag is an ElementNode.
		if node.Node.Type == html.ElementNode && node.Node.Data == "slot" {
			slotName := getAttr(node.Node, "name")
			var assignedNodes []*style.StyledNode

			if slotName == "" {
				// This is a default slot. It gets any slottable nodes
				// that didn't have a specific "slot" attribute.
				if len(defaultSlottables) > 0 {
					assignedNodes = defaultSlottables
					defaultSlottables = nil // Consume them so they can't be used again.
				}
			} else {
				// This is a named slot. Find the matching nodes.
				if nodes, ok := namedSlottables[slotName]; ok {
					assignedNodes = nodes
					delete(namedSlottables, slotName) // Consume them.
				}
			}
			// Assign the found nodes to the slot. If no nodes were found,
			// this will be an empty slice, and the slot's fallback content
			// (its own children) will be rendered instead.
			node.SlotAssignment = assignedNodes
		}

		// Continue the traversal through the shadow DOM.
		for _, child := range node.Children {
			traverseShadow(child)
		}
		// Also traverse the shadow root if it exists
		if node.ShadowRoot != nil {
			traverseShadow(node.ShadowRoot)
		}
	}

	traverseShadow(host.ShadowRoot)
}

// -- Helper Functions --

// getAttr is a case insensitive helper to retrieve an attribute's value from a node.
func getAttr(n *html.Node, key string) string {
	if n == nil {
		return ""
	}
	for _, attr := range n.Attr {
		if strings.EqualFold(attr.Key, key) {
			return attr.Val
		}
	}
	return ""
}

// cloneNode creates a deep copy of an html.Node and its descendants. This is vital
// for template instantiation, ensuring the original template node remains untouched.
func cloneNode(n *html.Node) *html.Node {
	if n == nil {
		return nil
	}

	newNode := &html.Node{
		Type:     n.Type,
		DataAtom: n.DataAtom,
		Data:     n.Data,
		Attr:     make([]html.Attribute, len(n.Attr)),
	}
	// Deep copy attributes to prevent slice modification issues.
	copy(newNode.Attr, n.Attr)

	// Recursively clone all children to build the new tree structure.
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		newNode.AppendChild(cloneNode(child))
	}

	return newNode
}
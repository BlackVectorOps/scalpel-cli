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
	if node == nil || node.Type != html.ElementNode {
		return false
	}

	for c := node.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == "template" {
			if getAttr(c, "shadowrootmode") != "" {
				return true
			}
		}
	}

	return false
}

// InstantiateShadowRoot finds the DSD template, clones its content to create a
// shadow tree, and extracts any encapsulated stylesheets.
func (e *Engine) InstantiateShadowRoot(host *html.Node) (*html.Node, []parser.StyleSheet) {
	var templateNode *html.Node
	for child := host.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && child.Data == "template" {
			if getAttr(child, "shadowrootmode") != "" {
				templateNode = child
				break
			}
		}
	}

	if templateNode == nil {
		return nil, nil // No DSD template found.
	}

	// The content to be cloned is either the direct children of the template
	// or the children of a document-fragment node inside the template.
	var contentSource *html.Node
	if templateNode.FirstChild != nil && templateNode.FirstChild.Type == html.DocumentNode {
		// Case 1: Content is wrapped in a document fragment.
		contentSource = templateNode.FirstChild
	} else {
		// Case 2: Content is composed of direct children of the template tag.
		contentSource = templateNode
	}

	if contentSource.FirstChild == nil {
		// Create an empty shadow root for templates that are empty.
		shadowRoot := &html.Node{
			Type: html.DocumentNode,
			Data: "shadow-root",
		}
		return shadowRoot, nil
	}

	// Create a new DocumentNode to serve as the root of our detached shadow tree.
	shadowRoot := &html.Node{
		Type: html.DocumentNode,
		Data: "shadow-root", // A marker for easier debugging.
	}

	// Clone all children from the identified content source into our new shadow root.
	for child := contentSource.FirstChild; child != nil; child = child.NextSibling {
		shadowRoot.AppendChild(cloneNode(child))
	}

	var stylesheets []parser.StyleSheet
	var nodesToRemove []*html.Node
	var findAndParseStyles func(*html.Node)
	findAndParseStyles = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "style" {
			if n.FirstChild != nil && n.FirstChild.Type == html.TextNode {
				p := parser.NewParser(n.FirstChild.Data)
				stylesheets = append(stylesheets, p.Parse())
			}
			nodesToRemove = append(nodesToRemove, n)
			return // Don't traverse into children of a <style> tag.
		}

		// Don't traverse into nested templates. They are inert.
		if n.Type == html.ElementNode && n.Data == "template" {
			return
		}

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

	// Step 1: Categorize all of the host's direct children (the "light DOM").
	namedSlottables := make(map[string][]*style.StyledNode)
	var defaultSlottables []*style.StyledNode

	for _, child := range host.Children {
		isSlottable := false
		slotName := ""

		if child.Node.Type == html.ElementNode {
			isSlottable = true
			slotName = getAttr(child.Node, "slot")
		} else if child.Node.Type == html.TextNode && strings.TrimSpace(child.Node.Data) != "" {
			isSlottable = true // Non-empty text nodes are slottable.
		}

		if isSlottable {
			if slotName != "" {
				namedSlottables[slotName] = append(namedSlottables[slotName], child)
			} else {
				defaultSlottables = append(defaultSlottables, child)
			}
		}
	}

	// Step 2: Traverse the shadow tree to find <slot> elements and assign nodes.
	var traverseShadow func(*style.StyledNode)
	traverseShadow = func(node *style.StyledNode) {
		if node == nil {
			return
		}

		// If we encounter another shadow host, stop. Slotting doesn't cross boundaries.
		if node != host.ShadowRoot && node.ShadowRoot != nil {
			return
		}

		if node.Node.Type == html.ElementNode && node.Node.Data == "slot" {
			slotName := getAttr(node.Node, "name")
			var assignedNodes []*style.StyledNode

			if slotName == "" {
				if len(defaultSlottables) > 0 {
					assignedNodes = defaultSlottables
					defaultSlottables = nil // Consume default slots.
				}
			} else {
				if nodes, ok := namedSlottables[slotName]; ok {
					assignedNodes = nodes
					delete(namedSlottables, slotName) // Consume named slots.
				}
			}
			node.SlotAssignment = assignedNodes
		}

		// Continue the traversal through the children of the current node.
		for _, child := range node.Children {
			traverseShadow(child)
		}
	}

	traverseShadow(host.ShadowRoot)
}

// -- Helper Functions --

// getAttr is a case-insensitive helper to retrieve an attribute's value from a node.
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

// cloneNode creates a deep copy of an html.Node and its descendants.
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
	copy(newNode.Attr, n.Attr)

	for child := n.FirstChild; child != nil; child = child.NextSibling {
		newNode.AppendChild(cloneNode(child))
	}

	return newNode
}
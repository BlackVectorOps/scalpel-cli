package browser

import (
	"fmt"
	"hash"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/chromedp/cdproto/cdp"
)

// interactiveElement is a helper struct to store a node and its pre-calculated fingerprint.
type interactiveElement struct {
	Node        *cdp.Node
	Fingerprint string
	Description string
	IsInput     bool
}

// hasherPool reuses FNV hasher instances to reduce memory allocations during fingerprinting.
var hasherPool = sync.Pool{
	New: func() interface{} {
		return fnv.New64a()
	},
}

// filterAndFingerprint identifies new elements and creates stable fingerprints for them.
func (i *Interactor) filterAndFingerprint(nodes []*cdp.Node, interacted map[string]bool, isInput bool) []interactiveElement {
	newElements := make([]interactiveElement, 0, len(nodes))
	for _, node := range nodes {
		attrs := attributeMap(node)
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

// generateNodeFingerprint creates a stable identifier for a DOM node.
func generateNodeFingerprint(node *cdp.Node, attrs map[string]string) (string, string) {
	var sb strings.Builder
	sb.WriteString(strings.ToLower(node.NodeName))

	if id, ok := attrs["id"]; ok && id != "" {
		sb.WriteString("#" + id)
	}
	if cls, ok := attrs["class"]; ok && cls != "" {
		classes := strings.Fields(cls)
		sort.Strings(classes)
		sb.WriteString("." + strings.Join(classes, "."))
	}

	attributesToInclude := []string{"name", "href", "type", "role", "aria-label", "placeholder", "title"}
	sort.Strings(attributesToInclude)
	for _, attr := range attributesToInclude {
		if val, ok := attrs[attr]; ok && val != "" {
			sb.WriteString(fmt.Sprintf(`[%s="%s"]`, attr, val))
		}
	}

	description := sb.String()
	if description == "" {
		return "", ""
	}

	hasher := hasherPool.Get().(hash.Hash64)
	defer func() {
		hasher.Reset()
		hasherPool.Put(hasher)
	}()

	_, _ = hasher.Write([]byte(description))
	hashVal := strconv.FormatUint(hasher.Sum64(), 16)
	return hashVal, description
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

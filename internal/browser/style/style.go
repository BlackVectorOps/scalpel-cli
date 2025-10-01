// internal/browser/style/style.go
package style

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/xkilldash9x/scalpel-cli/internal/browser/parser"
	"github.com/xkilldash9x/scalpel-cli/internal/observability"
	"go.uber.org/zap"
	"golang.org/x/net/html"
)

// -- Canonical Data Structures --

// StyledNode represents a DOM node combined with its computed styles. It is the
// bridge between the parser's output and the layout engine's input.
type StyledNode struct {
	Node           *html.Node
	ComputedStyles map[parser.Property]parser.Value
	Children       []*StyledNode
}

// GridTrackDefinition represents a single track definition, like "1fr" or "100px".
type GridTrackDefinition struct {
	Size      string
	LineNames []string
}

// GridLine represents a parsed placement value (e.g., from grid-column-start).
type GridLine struct {
	IsAuto      bool
	IsNamedSpan bool
	Span        int
	Line        int
	Name        string
}

// -- Value Lookup and Property Parsers --

// Lookup retrieves a style, falling back if not present.
func (sn *StyledNode) Lookup(property, fallback string) string {
	if val, ok := sn.ComputedStyles[parser.Property(property)]; ok {
		return string(val)
	}
	return fallback
}

// Definitions for various CSS properties.
type DisplayType int

const (
	DisplayInline DisplayType = iota
	DisplayBlock
	DisplayInlineBlock
	DisplayFlex
	DisplayGrid
	DisplayTable
	DisplayTableRow
	DisplayTableCell
	DisplayNone
)

// Display determines the layout mode.
func (sn *StyledNode) Display() DisplayType {
	if sn.Node.Type == html.TextNode {
		return DisplayInline
	}

	if display, ok := sn.ComputedStyles["display"]; ok {
		switch display {
		case "block":
			return DisplayBlock
		case "flex":
			return DisplayFlex
		case "grid":
			return DisplayGrid
		case "table":
			return DisplayTable
		case "table-row":
			return DisplayTableRow
		case "table-cell":
			return DisplayTableCell
		case "none":
			return DisplayNone
		case "inline-block":
			return DisplayInlineBlock
		case "inline":
			return DisplayInline
		}
	}
	return getDefaultDisplay(sn.Node)
}

type PositionType int

const (
	PositionStatic PositionType = iota
	PositionRelative
	PositionAbsolute
	PositionFixed
)

func (sn *StyledNode) Position() PositionType {
	switch sn.Lookup("position", "static") {
	case "relative":
		return PositionRelative
	case "absolute":
		return PositionAbsolute
	case "fixed":
		return PositionFixed
	default:
		return PositionStatic
	}
}

type FloatType int

const (
	FloatNone FloatType = iota
	FloatLeft
	FloatRight
)

func (sn *StyledNode) Float() FloatType {
	switch sn.Lookup("float", "none") {
	case "left":
		return FloatLeft
	case "right":
		return FloatRight
	default:
		return FloatNone
	}
}

type ClearType int

const (
	ClearNone ClearType = iota
	ClearLeft
	ClearRight
	ClearBoth
)

func (sn *StyledNode) Clear() ClearType {
	switch sn.Lookup("clear", "none") {
	case "left":
		return ClearLeft
	case "right":
		return ClearRight
	case "both":
		return ClearBoth
	default:
		return ClearNone
	}
}

// BoxSizingType determines how width/height are calculated.
type BoxSizingType int

const (
	ContentBox BoxSizingType = iota
	BorderBox
)

func (sn *StyledNode) BoxSizing() BoxSizingType {
	if sn.Lookup("box-sizing", "content-box") == "border-box" {
		return BorderBox
	}
	return ContentBox
}

type FlexDirection int

const (
	FlexDirectionRow FlexDirection = iota
	FlexDirectionRowReverse
	FlexDirectionColumn
	FlexDirectionColumnReverse
)

func (sn *StyledNode) GetFlexDirection() FlexDirection {
	switch sn.Lookup("flex-direction", "row") {
	case "column":
		return FlexDirectionColumn
	case "row-reverse":
		return FlexDirectionRowReverse
	case "column-reverse":
		return FlexDirectionColumnReverse
	default:
		return FlexDirectionRow
	}
}

type FlexWrap int

const (
	FlexNoWrap FlexWrap = iota
	FlexWrapValue
)

func (sn *StyledNode) GetFlexWrap() FlexWrap {
	if sn.Lookup("flex-wrap", "nowrap") == "wrap" {
		return FlexWrapValue
	}
	return FlexNoWrap
}

// IsVisible checks if the element is visually rendered.
func (sn *StyledNode) IsVisible() bool {
	if sn.Display() == DisplayNone {
		return false
	}
	visibility := sn.Lookup("visibility", "visible")
	if visibility == "hidden" || visibility == "collapse" {
		return false
	}
	// Check opacity.
	opacityStr := sn.Lookup("opacity", "1.0")
	if opacity, err := strconv.ParseFloat(opacityStr, 64); err == nil && opacity <= 0.0 {
		return false
	}
	return true
}

// A regex to find and parse repeat() functions.
var repeatRegex = regexp.MustCompile(`repeat\(\s*(\d+)\s*,\s*([^)]+)\)`)

// isWhitespace checks if a character is CSS whitespace.
func isWhitespace(r byte) bool {
	return r == ' ' || r == '\t' || r == '\n'
}

// tokenizeGridTracks splits a grid track definition string into its core components.
func tokenizeGridTracks(value string) []string {
	var tokens []string
	for i := 0; i < len(value); {
		if isWhitespace(value[i]) {
			i++
			continue
		}
		if value[i] == '[' {
			start := i
			end := strings.IndexRune(value[start:], ']')
			if end == -1 {
				tokens = append(tokens, value[start:])
				break
			}
			tokens = append(tokens, value[start:start+end+1])
			i = start + end + 1
		} else {
			start := i
			parenDepth := 0
			inQuotes := false
			var quoteChar byte = ' '

			for ; i < len(value); i++ {
				char := value[i]
				if (char == '"' || char == '\'') && !inQuotes {
					inQuotes = true
					quoteChar = char
				} else if char == quoteChar && inQuotes {
					inQuotes = false
				}
				if char == '(' && !inQuotes {
					parenDepth++
				} else if char == ')' && !inQuotes {
					parenDepth--
				} else if isWhitespace(char) && parenDepth == 0 && !inQuotes {
					break
				}
			}
			tokens = append(tokens, value[start:i])
		}
	}
	return tokens
}

// GetGridTemplateTracks parses properties like `grid-template-columns`.
func (sn *StyledNode) GetGridTemplateTracks(property string) ([]GridTrackDefinition, []string) {
	value := sn.Lookup(property, "none")
	if value == "none" || value == "" {
		return nil, nil
	}
	expandedValue := repeatRegex.ReplaceAllStringFunc(value, func(match string) string {
		submatches := repeatRegex.FindStringSubmatch(match)
		if len(submatches) < 3 {
			observability.GetLogger().Warn("Malformed repeat() function", zap.String("match", match))
			return ""
		}
		count, err := strconv.Atoi(submatches[1])
		if err != nil {
			observability.GetLogger().Warn("Invalid count in repeat()", zap.String("count_val", submatches[1]), zap.Error(err))
			return ""
		}
		tracksToRepeat := submatches[2]
		return strings.TrimSpace(strings.Repeat(tracksToRepeat+" ", count))
	})

	var definitions []GridTrackDefinition
	var currentNames []string
	tokens := tokenizeGridTracks(expandedValue)

	for _, token := range tokens {
		if strings.HasPrefix(token, "[") {
			names := strings.Fields(strings.Trim(token, "[]"))
			currentNames = append(currentNames, names...)
		} else {
			definitions = append(definitions, GridTrackDefinition{
				Size:      token,
				LineNames: currentNames,
			})
			currentNames = nil
		}
	}
	return definitions, currentNames
}

// ParseGridLine parses a single grid placement value (like for grid-column-start).
func (sn *StyledNode) ParseGridLine(property, fallback string) GridLine {
	value := strings.TrimSpace(sn.Lookup(property, fallback))

	if value == "auto" {
		return GridLine{IsAuto: true}
	}
	if strings.HasPrefix(value, "span ") {
		spanValue := strings.TrimSpace(strings.TrimPrefix(value, "span "))
		if span, err := strconv.Atoi(spanValue); err == nil {
			return GridLine{Span: span}
		}
		return GridLine{Name: spanValue, IsNamedSpan: true}
	}
	if line, err := strconv.Atoi(value); err == nil {
		return GridLine{Line: line}
	}
	return GridLine{Name: value}
}

// getDefaultDisplay provides a basic User Agent stylesheet equivalent.
func getDefaultDisplay(node *html.Node) DisplayType {
	if node.Type != html.ElementNode {
		return DisplayInline
	}
	switch strings.ToLower(node.Data) {
	case "html", "body", "div", "p", "h1", "h2", "h3", "h4", "h5", "h6",
		"ul", "ol", "li", "form", "header", "footer", "section", "article", "nav", "main":
		return DisplayBlock
	case "table":
		return DisplayTable
	case "tr":
		return DisplayTableRow
	case "td", "th":
		return DisplayTableCell
	case "input", "button", "textarea", "select", "img":
		return DisplayInlineBlock
	default:
		return DisplayInline
	}
}


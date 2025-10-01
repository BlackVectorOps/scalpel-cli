// internal/browser/layout/layout_test.go
package layout

import (
	"strings"
	"testing"

	"github.com/antchfx/htmlquery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xkilldash9x/scalpel-cli/internal/browser/parser"
)

// -- Test Helpers --

// setupLayoutTest is a convenience function to parse HTML and CSS, and run the layout engine.
func setupLayoutTest(t *testing.T, htmlString, cssString string, viewportWidth, viewportHeight float64) (*Engine, *LayoutBox) {
	t.Helper()

	doc, err := htmlquery.Parse(strings.NewReader(htmlString))
	require.NoError(t, err, "Failed to parse test HTML")

	engine := NewEngine()
	if cssString != "" {
		p := parser.NewParser(cssString)
		stylesheet := p.Parse()
		// FIX: Use the new public field `authorSheets` instead of the old method.
		engine.authorSheets = append(engine.authorSheets, stylesheet)
	}

	layoutRoot := engine.Render(doc, viewportWidth, viewportHeight)
	require.NotNil(t, layoutRoot, "Layout root should not be nil")

	return engine, layoutRoot
}

// -- Test Cases --

// TestFlexboxLayout_JustifyAndAlign verifies the core alignment properties of Flexbox.
func TestFlexboxLayout_JustifyAndAlign(t *testing.T) {
	html := `
	<div id="container">
	  <div id="item1"></div>
	  <div id="item2"></div>
	  <div id="item3"></div>
	</div>
	`
	css := `
	#container {
		width: 500px;
		height: 100px;
		display: flex;
		padding: 10px; /* Affects content width */
		justify-content: space-between;
		align-items: center;
	}
	#item1 { width: 50px; height: 50px; }
	#item2 { width: 50px; height: 80px; }
	#item3 { width: 50px; height: 30px; }
	`
	engine, root := setupLayoutTest(t, html, css, 600, 400)

	// -- Assertions --
	// Container content width is 500px, so items are laid out within that.
	// The container itself is at X=10 due to padding.
	geo1, err1 := engine.GetElementGeometry(root, "//*[@id='item1']")
	geo2, err2 := engine.GetElementGeometry(root, "//*[@id='item2']")
	geo3, err3 := engine.GetElementGeometry(root, "//*[@id='item3']")

	require.NoError(t, err1)
	require.NoError(t, err2)
	require.NoError(t, err3)

	// Verify justify-content: space-between
	// Item 1 should be at the start of the content box (x=10).
	assert.InDelta(t, 10.0, geo1.Vertices[0], 0.1, "Item 1 X position")
	// Item 3 should be at the end (500 - 50 + 10 padding = 460).
	assert.InDelta(t, 460.0, geo3.Vertices[0], 0.1, "Item 3 X position")
	// Item 2 should be in the middle of the remaining space.
	// (500 - 150) / 2 + 50 + 10 padding = 175 + 50 + 10 = 235
	assert.InDelta(t, 235.0, geo2.Vertices[0], 0.1, "Item 2 X position")

	// Verify align-items: center
	// Container content height is 100px. Y starts at 10.
	// Item 1 (50px high): (100 - 50)/2 + 10 = 35
	assert.InDelta(t, 35.0, geo1.Vertices[1], 0.1, "Item 1 Y position")
	// Item 2 (80px high): (100 - 80)/2 + 10 = 20
	assert.InDelta(t, 20.0, geo2.Vertices[1], 0.1, "Item 2 Y position")
	// Item 3 (30px high): (100 - 30)/2 + 10 = 45
	assert.InDelta(t, 45.0, geo3.Vertices[1], 0.1, "Item 3 Y position")
}

// TestAbsolutePositioning verifies an element is positioned relative to its containing block.
func TestAbsolutePositioning(t *testing.T) {
	html := `
	<div id="container">
	  <div id="absolute-child"></div>
	</div>
	`
	css := `
	#container {
		position: relative;
		width: 300px;
		height: 300px;
		margin: 50px;
		padding: 20px;
		border: 10px solid black;
	}
	#absolute-child {
		position: absolute;
		top: 15px;
		left: 25px;
		width: 40px;
		height: 40px;
	}
	`
	engine, root := setupLayoutTest(t, html, css, 600, 400)

	geo, err := engine.GetElementGeometry(root, "//*[@id='absolute-child']")
	require.NoError(t, err)

	// The containing block for an absolutely positioned element is the PADDING box of the nearest positioned ancestor.
	// Container starts at X=50(margin), Y=50(margin).
	// Its padding box starts at X=50+10(border)=60, Y=50+10=60.
	// The child should be at:
	// X = 60 (container padding-box start) + 25 (left property) = 85
	// Y = 60 (container padding-box start) + 15 (top property) = 75
	assert.InDelta(t, 85.0, geo.Vertices[0], 0.1, "Absolute X position")
	assert.InDelta(t, 75.0, geo.Vertices[1], 0.1, "Absolute Y position")
	assert.Equal(t, int64(40), geo.Width, "Absolute width")
	assert.Equal(t, int64(40), geo.Height, "Absolute height")
}

// TestGridLayout_ExplicitPlacement verifies a simple grid with explicitly placed items.
func TestGridLayout_ExplicitPlacement(t *testing.T) {
	html := `
	<div id="grid">
	  <div id="itemA">A</div>
	  <div id="itemB">B</div>
	</div>
	`
	css := `
	#grid {
		display: grid;
		width: 400px;
		height: 300px;
		grid-template-columns: 100px 1fr;
		grid-template-rows: 50px 1fr;
	}
	#itemA {
		grid-column-start: 1;
		grid-row-start: 1;
	}
	#itemB {
		grid-column-start: 2;
		grid-row-start: 2;
	}
	`
	engine, root := setupLayoutTest(t, html, css, 600, 400)

	geoA, errA := engine.GetElementGeometry(root, "//*[@id='itemA']")
	geoB, errB := engine.GetElementGeometry(root, "//*[@id='itemB']")
	require.NoError(t, errA)
	require.NoError(t, errB)

	// Item A: first column (100px wide), first row (50px high).
	assert.InDelta(t, 0.0, geoA.Vertices[0], 0.1, "Item A X position")
	assert.InDelta(t, 0.0, geoA.Vertices[1], 0.1, "Item A Y position")
	assert.Equal(t, int64(100), geoA.Width, "Item A width")
	assert.Equal(t, int64(50), geoA.Height, "Item A height")

	// Item B: second column (1fr = 300px), second row (1fr = 250px).
	assert.InDelta(t, 100.0, geoB.Vertices[0], 0.1, "Item B X position")
	assert.InDelta(t, 50.0, geoB.Vertices[1], 0.1, "Item B Y position")
	assert.Equal(t, int64(300), geoB.Width, "Item B width")
	assert.Equal(t, int64(250), geoB.Height, "Item B height")
}

// TestTransforms verifies that CSS transforms are correctly applied to the final vertices.
func TestTransforms(t *testing.T) {
	html := `<div id="transformed"></div>`
	css := `
	#transformed {
		width: 100px;
		height: 100px;
		transform-origin: 0 0; /* Top-left corner for simple calculation */
		transform: translate(50px, 50px) rotate(90deg);
	}
	`
	engine, root := setupLayoutTest(t, html, css, 600, 400)

	geo, err := engine.GetElementGeometry(root, "//*[@id='transformed']")
	require.NoError(t, err)

	// Original corners: (0,0), (100,0), (100,100), (0,100)
	// After rotate(90deg) around (0,0): (0,0), (0,100), (-100,100), (-100,0)
	// After translate(50, 50):
	// Top-left: (0+50, 0+50) = (50, 50)
	// Top-right: (0+50, 100+50) = (50, 150)
	// Bottom-right: (-100+50, 100+50) = (-50, 150)
	// Bottom-left: (-100+50, 0+50) = (-50, 50)

	// Vertices are clockwise from top-left.
	assert.InDelta(t, 50.0, geo.Vertices[0], 0.1, "Transformed vertex 1 (X)")
	assert.InDelta(t, 50.0, geo.Vertices[1], 0.1, "Transformed vertex 1 (Y)")

	assert.InDelta(t, 50.0, geo.Vertices[2], 0.1, "Transformed vertex 2 (X)")
	assert.InDelta(t, 150.0, geo.Vertices[3], 0.1, "Transformed vertex 2 (Y)")

	assert.InDelta(t, -50.0, geo.Vertices[4], 0.1, "Transformed vertex 3 (X)")
	assert.InDelta(t, 150.0, geo.Vertices[5], 0.1, "Transformed vertex 3 (Y)")

	assert.InDelta(t, -50.0, geo.Vertices[6], 0.1, "Transformed vertex 4 (X)")
	assert.InDelta(t, 50.0, geo.Vertices[7], 0.1, "Transformed vertex 4 (Y)")

	// The AABB width and height should still be 100x100 for a 90-degree rotation.
	assert.Equal(t, int64(100), geo.Width)
	assert.Equal(t, int64(100), geo.Height)
}



// File: internal/jsoncompare/service_test.go
package jsoncompare_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xkilldash9x/scalpel-cli/internal/jsoncompare"
)

// Basic test suite to cover core functionalities.

func TestJSONCompareService_Basic(t *testing.T) {
	service := jsoncompare.NewService()

	jsonA := `{"id": 1, "name": "Alice", "session_token": "abc-high-entropy-value-123", "ts": "2025-01-01T10:00:00Z"}`
	// Different dynamic values (token and timestamp) normalized by default heuristics
	jsonB := `{"id": 1, "name": "Alice", "session_token": "xyz-another-entropy-value-987", "ts": "2025-10-13T15:00:00Z"}`
	// Structurally different
	jsonC := `{"id": 1, "name": "Alice", "active": true}`

	t.Run("Identical after normalization", func(t *testing.T) {
		result, err := service.Compare([]byte(jsonA), []byte(jsonB))
		require.NoError(t, err)
		assert.True(t, result.AreEquivalent)
		assert.True(t, result.IsJSON)
	})

	t.Run("Structurally different", func(t *testing.T) {
		result, err := service.Compare([]byte(jsonA), []byte(jsonC))
		require.NoError(t, err)
		assert.False(t, result.AreEquivalent)
		assert.NotEmpty(t, result.Diff)
	})

	t.Run("Non-JSON input comparison (different)", func(t *testing.T) {
		htmlA := `<html>A</html>`
		htmlB := `<html>B</html>`
		result, err := service.Compare([]byte(htmlA), []byte(htmlB))
		require.NoError(t, err)
		assert.False(t, result.AreEquivalent)
		assert.False(t, result.IsJSON)
	})

	t.Run("Identical Non-JSON input", func(t *testing.T) {
		htmlA := `<html>A</html>`
		result, err := service.Compare([]byte(htmlA), []byte(htmlA))
		require.NoError(t, err)
		assert.True(t, result.AreEquivalent)
		assert.False(t, result.IsJSON)
	})
}

func TestJSONCompareService_Structural(t *testing.T) {
	service := jsoncompare.NewService()
	opts := jsoncompare.DefaultOptions()
	// Enable structural comparison
	opts.NormalizeAllValuesForStructure = true

	jsonA := `{"user_id": 100, "item_count": 5, "status": "active"}`
	jsonB := `{"user_id": 200, "item_count": 10, "status": "inactive"}` // Different data, same structure
	jsonC := `{"user_id": 300, "status": "pending"}`                    // Different structure

	t.Run("Same Structure", func(t *testing.T) {
		result, err := service.CompareWithOptions([]byte(jsonA), []byte(jsonB), opts)
		require.NoError(t, err)
		assert.True(t, result.AreEquivalent, "Should be equivalent when comparing structure only")
	})

	t.Run("Different Structure", func(t *testing.T) {
		result, err := service.CompareWithOptions([]byte(jsonA), []byte(jsonC), opts)
		require.NoError(t, err)
		assert.False(t, result.AreEquivalent, "Should not be equivalent when structure differs")
	})
}

func TestJSONCompareService_Options(t *testing.T) {
	service := jsoncompare.NewService()
	jsonA := `{"list": [1, 2], "empty": {}}`
	jsonB := `{"list": [2, 1], "empty": null}`

	t.Run("Default Options (Ignore Order, Equate Empty)", func(t *testing.T) {
		result, err := service.Compare([]byte(jsonA), []byte(jsonB))
		require.NoError(t, err)
		assert.True(t, result.AreEquivalent)
	})

	t.Run("Strict Order", func(t *testing.T) {
		opts := jsoncompare.DefaultOptions()
		opts.IgnoreArrayOrder = false
		result, err := service.CompareWithOptions([]byte(jsonA), []byte(jsonB), opts)
		require.NoError(t, err)
		assert.False(t, result.AreEquivalent)
	})

	t.Run("Strict Empty", func(t *testing.T) {
		opts := jsoncompare.DefaultOptions()
		opts.EquateEmpty = false
		result, err := service.CompareWithOptions([]byte(jsonA), []byte(jsonB), opts)
		require.NoError(t, err)
		assert.False(t, result.AreEquivalent)
	})
}

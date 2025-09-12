package schemas_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	// Third party libraries for expressive and robust assertions.
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	// Import the package we are testing.
	"github.com/xkilldash9x/scalpel-cli/api/schemas"
)

// -- Test Helpers --

// getTestTime provides a fixed, reproducible timestamp for consistent test results.
func getTestTime(t *testing.T) time.Time {
	// Using RFC3339Nano ensures maximum precision, and UTC avoids timezone issues.
	ts, err := time.Parse(time.RFC3339Nano, "2025-10-26T10:00:00.123456789Z")
	require.NoError(t, err, "Test setup failed: unable to parse fixed timestamp")
	return ts
}

// -- Test Cases --

// TestConstants verifies that all defined constants hold their expected string values.
// This is a good way to prevent accidental changes to values that might be used in APIs.
func TestConstants(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		constant interface{} // Use interface{} to handle various constant types
		expected string
	}{
		// TaskTypes
		{"TaskAgentMission", schemas.TaskAgentMission, "AGENT_MISSION"},
		{"TaskAnalyzeWebPageTaint", schemas.TaskAnalyzeWebPageTaint, "ANALYZE_WEB_PAGE_TAINT"},
		{"TaskAnalyzeHeaders", schemas.TaskAnalyzeHeaders, "ANALYZE_HEADERS"},
		{"TaskAnalyzeJWT", schemas.TaskAnalyzeJWT, "ANALYZE_JWT"},

		// Severities
		{"SeverityCritical", schemas.SeverityCritical, "CRITICAL"},
		{"SeverityHigh", schemas.SeverityHigh, "HIGH"},
		{"SeverityInformational", schemas.SeverityInformational, "INFORMATIONAL"},

		// LLM ModelTiers
		{"TierFast", schemas.TierFast, "fast"},
		{"TierPowerful", schemas.TierPowerful, "powerful"},
	}

	for _, tc := range testCases {
		// Capture range variable for parallel execution.
		tt := tc
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Dynamically resolve the string representation of the constant.
			var actual string
			if stringer, ok := tt.constant.(fmt.Stringer); ok {
				actual = stringer.String()
			} else {
				// Fallback for basic types like string aliases.
				actual = fmt.Sprintf("%v", tt.constant)
			}
			assert.Equal(t, tt.expected, actual)
		})
	}
}

// TestStructJSONTags uses reflection to verify that the `json` tags on struct fields
// are correct. This is critical for ensuring API contract stability.
func TestStructJSONTags(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name         string
		structRef    interface{}
		expectedTags map[string]string
	}{
		{
			name:      "Finding",
			structRef: schemas.Finding{},
			expectedTags: map[string]string{
				"ID":             "id",
				"TaskID":         "task_id",
				"Timestamp":      "timestamp",
				"Target":         "target",
				"Module":         "module",
				"Vulnerability":  "vulnerability",
				"Severity":       "severity",
				"Description":    "description",
				"Evidence":       "evidence",
				"Recommendation": "recommendation",
				"CWE":            "cwe",
			},
		},
		{
			name:      "ResultEnvelope",
			structRef: schemas.ResultEnvelope{},
			expectedTags: map[string]string{
				"ScanID":    "scan_id",
				"TaskID":    "task_id",
				"Timestamp": "timestamp",
				"Findings":  "findings",
				"KGUpdates": "kg_updates",
			},
		},
		{
			name:      "NodeInput",
			structRef: schemas.NodeInput{},
			expectedTags: map[string]string{
				"ID":         "id",
				"Type":       "type",
				"Properties": "properties",
			},
		},
		{
			name:      "EdgeInput",
			structRef: schemas.EdgeInput{},
			expectedTags: map[string]string{
				"SourceID":     "source_id",
				"TargetID":     "target_id",
				"Relationship": "relationship",
				"Properties":   "properties",
			},
		},
		{
			name:      "GenerationOptions",
			structRef: schemas.GenerationOptions{},
			expectedTags: map[string]string{
				"Temperature":     "temperature",
				"ForceJSONFormat": "force_json_format",
			},
		},
		{
			name:      "GenerationRequest",
			structRef: schemas.GenerationRequest{},
			expectedTags: map[string]string{
				"SystemPrompt": "system_prompt",
				"UserPrompt":   "user_prompt",
				"Tier":         "tier",
				"Options":      "options",
			},
		},
	}

	for _, tc := range testCases {
		tt := tc
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			structType := reflect.TypeOf(tt.structRef)
			for fieldName, expectedTag := range tt.expectedTags {
				field, found := structType.FieldByName(fieldName)
				require.True(t, found, "Field '%s' not found in struct '%s'.", fieldName, tt.name)
				actualTag := field.Tag.Get("json")
				assert.Contains(t, actualTag, expectedTag, "JSON tag mismatch for field '%s.%s'", tt.name, fieldName)
			}
		})
	}
}

// TestSerializationCycle performs a round trip test (marshal to JSON and unmarshal back).
// It verifies that data integrity is maintained, which is essential for data transfer.
func TestSerializationCycle(t *testing.T) {
	t.Parallel()
	timestamp := getTestTime(t)

	// NOTE: When Go's json library unmarshals into an interface{}, it converts all JSON
	// numbers to float64. To ensure a successful reflect.DeepEqual comparison,
	// the original structs must also use float64 for numeric types in these maps.
	finding := schemas.Finding{
		ID:          "finding-001",
		TaskID:      "task-abc",
		Timestamp:   timestamp,
		Target:      "https://example.com/login",
		Module:      "PassiveHeaderAnalysis",
		Description: "Missing Content-Security-Policy header.",
		Evidence:    "Response headers did not contain CSP.",
		Vulnerability: schemas.Vulnerability{
			Name:        "Missing Security Header",
			Description: "Content Security Policy (CSP) is an added layer of security that helps to detect and mitigate certain types of attacks, including Cross-Site Scripting (XSS) and data injection attacks.",
		},
		Severity:       schemas.SeverityMedium,
		Recommendation: "Implement a strict Content-Security-Policy header.",
		CWE:            []string{"CWE-693"},
	}

	nodeInput := schemas.NodeInput{
		ID:   "host-123",
		Type: schemas.NodeType("Host"),
		Properties: map[string]interface{}{
			"ip":     "192.168.1.100",
			"ports":  []interface{}{float64(80), float64(443)}, // Use float64 for numbers
			"active": true,
		},
	}

	edgeInput := schemas.EdgeInput{
		SourceID:     "host-123",
		TargetID:     "service-80",
		Relationship: schemas.RelationshipType("EXPOSES"),
		Properties: map[string]interface{}{
			"protocol": "tcp",
		},
	}

	envelope := schemas.ResultEnvelope{
		ScanID:    "scan-xyz",
		TaskID:    "task-abc",
		Timestamp: timestamp,
		Findings:  []schemas.Finding{finding},
		KGUpdates: &schemas.KnowledgeGraphUpdate{
			Nodes: []schemas.NodeInput{nodeInput},
			Edges: []schemas.EdgeInput{edgeInput},
		},
	}

	t.Run("ResultEnvelope", func(t *testing.T) {
		data, err := json.Marshal(envelope)
		require.NoError(t, err, "Marshalling ResultEnvelope should not fail")

		var unmarshaled schemas.ResultEnvelope
		err = json.Unmarshal(data, &unmarshaled)
		require.NoError(t, err, "Unmarshalling ResultEnvelope should not fail")

		// reflect.DeepEqual provides a robust, recursive comparison.
		assert.True(t, reflect.DeepEqual(envelope, unmarshaled), "Original and unmarshaled objects should be identical")
	})

	t.Run("GenerationRequest", func(t *testing.T) {
		request := schemas.GenerationRequest{
			SystemPrompt: "System instructions.",
			UserPrompt:   "User query.",
			Tier:         schemas.TierPowerful,
			Options: schemas.GenerationOptions{
				Temperature:     0.3,
				ForceJSONFormat: true,
			},
		}

		data, err := json.Marshal(request)
		require.NoError(t, err, "Marshalling GenerationRequest should not fail")

		var unmarshaledRequest schemas.GenerationRequest
		err = json.Unmarshal(data, &unmarshaledRequest)
		require.NoError(t, err, "Unmarshalling GenerationRequest should not fail")

		assert.True(t, reflect.DeepEqual(request, unmarshaledRequest), "Original and unmarshaled GenerationRequest should be identical")
	})
}

// TestInterfaceHandlingBehavior explicitly verifies how the json library decodes
// different JSON types into the `map[string]interface{}` used in our schemas.
func TestInterfaceHandlingBehavior(t *testing.T) {
	t.Parallel()
	inputJSON := `{
        "id": "behavior-test-node",
        "type": "Test",
        "properties": {
            "intVal": 42,
            "floatVal": 3.14,
            "arrayVal": [100, "mixed", true],
            "objectVal": {"nested": "value"}
        }
    }`

	var node schemas.NodeInput
	err := json.Unmarshal([]byte(inputJSON), &node)
	require.NoError(t, err, "Unmarshalling for behavior test should succeed")

	props := node.Properties
	require.NotNil(t, props, "Properties map should not be nil")

	// JSON numbers (int or float) are decoded into float64.
	assert.Equal(t, float64(42), props["intVal"], "Integer value should be decoded as float64")
	assert.Equal(t, 3.14, props["floatVal"], "Float value should be decoded as float64")

	// JSON arrays are decoded into []interface{}.
	expectedArray := []interface{}{float64(100), "mixed", true}
	assert.Equal(t, expectedArray, props["arrayVal"], "Array should be decoded as []interface{} with correct types")

	// JSON objects are decoded into map[string]interface{}.
	expectedObject := map[string]interface{}{"nested": "value"}
	assert.Equal(t, expectedObject, props["objectVal"], "Object should be decoded as map[string]interface{}")
}

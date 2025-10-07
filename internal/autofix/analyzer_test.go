// internal/autofix/analyzer_test.go
package autofix

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"
)

// Helper to setup Analyzer and Mocks
func setupAnalyzer(t *testing.T) (*Analyzer, *MockLLMClient, string) {
	t.Helper()
	logger := zaptest.NewLogger(t)
	mockLLM := new(MockLLMClient)
	projectRoot := t.TempDir()
	analyzer := NewAnalyzer(logger, mockLLM, projectRoot)
	return analyzer, mockLLM, projectRoot
}

// Helper to create source files
func createSourceFile(t *testing.T, root, path, content string) {
	t.Helper()
	fullPath := filepath.Join(root, path)
	require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0755))
	require.NoError(t, os.WriteFile(fullPath, []byte(content), 0644))
}

// TestAnalyzer_GeneratePatch_Success tests the E2E flow.
func TestAnalyzer_GeneratePatch_Success(t *testing.T) {
	analyzer, mockLLM, projectRoot := setupAnalyzer(t)

	// 1. Setup: Create source file and report
	filePath := "service.go"
	sourceCode := "package main\n\nfunc main() {\n\tvar x *int\n\t*x = 5 // Line 5\n}\n"
	createSourceFile(t, projectRoot, filePath, sourceCode)

	report := PostMortem{
		IncidentID: "INC123", FilePath: filePath, LineNumber: 5, PanicMessage: "nil pointer",
	}

	// 2. Define expected response and mock behavior
	expectedPatch := `--- a/service.go
+++ b/service.go
@@ -2,5 +2,6 @@

 func main() {
 	var x *int
+    x = new(int)
 	*x = 5 // Line 5
 }`
	analysis := AnalysisResult{
		Explanation: "Fixed.", RootCause: "Nil pointer", Confidence: 0.95, Patch: expectedPatch,
	}
	responseJSON, _ := json.Marshal(analysis)

	// Use mock.MatchedBy to validate the prompt content sent to the LLM
	mockLLM.On("Generate", mock.Anything, mock.MatchedBy(func(req schemas.GenerationRequest) bool {
		return strings.Contains(req.UserPrompt, "-> 5: \t*x = 5 // Line 5") &&
			req.Tier == schemas.TierPowerful &&
			req.Options.ForceJSONFormat == true
	})).Return(string(responseJSON), nil).Once()

	// 3. Execute
	result, err := analyzer.GeneratePatch(context.Background(), report)

	// 4. Assert
	require.NoError(t, err)
	assert.Equal(t, expectedPatch, result.Patch)
	assert.Equal(t, 0.95, result.Confidence)
	mockLLM.AssertExpectations(t)
}

// DE LUX FEATURE: Testing Adherence to Context Best Practices
func TestAnalyzer_ContextHandling(t *testing.T) {
	analyzer, mockLLM, projectRoot := setupAnalyzer(t)
	createSourceFile(t, projectRoot, "file.go", "package main")
	report := PostMortem{FilePath: "file.go", LineNumber: 1}

	// 1. Test Parent Context Cancellation
	t.Run("Best Practice - Parent Context Cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := analyzer.GeneratePatch(ctx, report)

		assert.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
	})

	// 2. Test Internal Timeout Configuration
	t.Run("Best Practice - Internal Timeout Configuration", func(t *testing.T) {
		ctxChan := make(chan context.Context, 1)

		mockLLM.On("Generate", mock.AnythingOfType("*context.timerCtx"), mock.Anything).Run(func(args mock.Arguments) {
			ctxChan <- args.Get(0).(context.Context)
		}).Return(`{"explanation":"E","root_cause":"R","confidence":0.9,"patch":"--- a/f\n+++ b/f\n@@ -1 +1 @@\n-o\n+n\n"}`, nil).Once()

		startTime := time.Now()
		parentCtx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		t.Cleanup(cancel)

		analyzer.GeneratePatch(parentCtx, report)

		llmCtx := <-ctxChan
		deadline, ok := llmCtx.Deadline()
		require.True(t, ok, "Context passed to LLM must have a deadline")

		expectedDeadline := startTime.Add(5 * time.Minute)
		assert.WithinDuration(t, expectedDeadline, deadline, 2*time.Second)
	})
}

// TestParseLLMResponse rigorously tests parsing robustness using TDTs.
func TestParseLLMResponse(t *testing.T) {
	t.Parallel()
	logger := zaptest.NewLogger(t)
	a := &Analyzer{logger: logger}

	// FIX: Removed trailing newlines from patch strings.
	validPatch := `--- a/file.go
+++ b/file.go
@@ -1,1 +1,1 @@
-old
+new`
	// Helper to generate JSON input easily
	generateInput := func(exp, rc string, conf float64, patch string) string {
		data, _ := json.Marshal(AnalysisResult{
			Explanation: exp,
			RootCause:   rc,
			Confidence:  conf,
			Patch:       patch,
		})
		return string(data)
	}

	tests := []struct {
		name    string
		input   string
		want    *AnalysisResult
		wantErr bool
	}{
		{
			name:  "Valid JSON",
			input: generateInput("Exp", "RC", 0.95, validPatch),
			want:  &AnalysisResult{Explanation: "Exp", RootCause: "RC", Confidence: 0.95, Patch: validPatch},
		},
		{
			name:  "Valid JSON wrapped in markdown (```json)",
			input: "```json\n" + generateInput("Exp", "RC", 0.95, validPatch) + "\n```",
			want:  &AnalysisResult{Explanation: "Exp", RootCause: "RC", Confidence: 0.95, Patch: validPatch},
		},
		{
			name:  "Patch wrapped in markdown (```diff)",
			input: generateInput("E", "R", 0.8, "```diff\n"+validPatch+"\n```"),
			want:  &AnalysisResult{Explanation: "E", RootCause: "R", Confidence: 0.8, Patch: validPatch},
		},
		{
			name:  "Confidence clamping (High)",
			input: generateInput("E", "R", 5.0, validPatch),
			want:  &AnalysisResult{Explanation: "E", RootCause: "R", Confidence: 1.0, Patch: validPatch},
		},
		{
			name:  "Confidence clamping (Low)",
			input: generateInput("E", "R", -1.0, validPatch),
			want:  &AnalysisResult{Explanation: "E", RootCause: "R", Confidence: 0.01, Patch: validPatch},
		},
		{
			name:    "Invalid JSON format",
			input:   `{"explanation": "missing quote}`,
			wantErr: true,
		},
		{
			name:    "Missing required field (Patch)",
			input:   `{"explanation": "...", "root_cause": "...", "confidence": 0.9}`,
			wantErr: true,
		},
		{
			name:    "Invalid patch format (not unified diff)",
			input:   generateInput("E", "R", 0.9, "just some code"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := a.parseLLMResponse(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseLLMResponse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("parseLLMResponse() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestExtractCodeContext(t *testing.T) {
	t.Parallel()
	sourceCode := `Line 1
Line 2
Line 3
Line 4 (Panic)
Line 5
Line 6
Line 7`

	tests := []struct {
		name        string
		lineNum     int
		contextSize int
		expected    string
	}{
		{
			"Middle of File", 4, 5,
			`   2: Line 2
   3: Line 3
-> 4: Line 4 (Panic)
   5: Line 5
   6: Line 6`,
		},
		{
			"Start of File", 1, 4,
			`-> 1: Line 1
   2: Line 2
   3: Line 3
   4: Line 4 (Panic)`,
		},
		{
			"Context Larger than File", 3, 20,
			`   1: Line 1
   2: Line 2
-> 3: Line 3
   4: Line 4 (Panic)
   5: Line 5
   6: Line 6
   7: Line 7`,
		},
		{"Invalid Line Number", 10, 4, "// Context unavailable: Invalid line number."},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := extractCodeContext(sourceCode, tt.lineNum, tt.contextSize)
			if diff := cmp.Diff(tt.expected, result); diff != "" {
				t.Errorf("extractCodeContext() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
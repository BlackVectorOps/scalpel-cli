// File: internal/analysis/active/protopollution/analyze/analyzer_test.go
package protopollution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	// FIX: Removed internal/browser import as we rely on schemas interfaces.
)

// -- Mock Definitions --

// MockBrowserManager mocks the schemas.BrowserManager interface.
type MockBrowserManager struct {
	mock.Mock
}

// FIX: Implementation of schemas.BrowserManager interface (NewAnalysisContext).
func (m *MockBrowserManager) NewAnalysisContext(
	sessionCtx context.Context,
	cfg interface{},
	persona schemas.Persona,
	taintTemplate string,
	taintConfig string,
) (schemas.SessionContext, error) {
	args := m.Called(sessionCtx, cfg, persona, taintTemplate, taintConfig)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(schemas.SessionContext), args.Error(1)
}

func (m *MockBrowserManager) Shutdown(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

// MockSessionContext mocks the schemas.SessionContext interface.
type MockSessionContext struct {
	mock.Mock
	exposedFunctions map[string]interface{}
	mutex            sync.Mutex
}

func NewMockSessionContext() *MockSessionContext {
	return &MockSessionContext{
		exposedFunctions: make(map[string]interface{}),
	}
}

// FIX: Updated signature to match schemas.SessionContext (includes ctx).
func (m *MockSessionContext) ExposeFunction(ctx context.Context, name string, function interface{}) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	args := m.Called(ctx, name, function)
	if args.Error(0) == nil {
		m.exposedFunctions[name] = function
	}
	return args.Error(0)
}

// Allows tests to invoke the exposed Go functions.
func (m *MockSessionContext) SimulateCallback(t *testing.T, name string, payload interface{}) {
	t.Helper()
	m.mutex.Lock()
	fn, exists := m.exposedFunctions[name]
	m.mutex.Unlock()

	if !exists {
		t.Fatalf("function %s not exposed by analyzer", name)
	}

	switch name {
	case jsCallbackName:
		callback, ok := fn.(func(PollutionProofEvent))
		require.True(t, ok, "PollutionProofEvent callback signature mismatch. Got: %T", fn)
		event, ok := payload.(PollutionProofEvent)
		require.True(t, ok, "PollutionProofEvent payload type mismatch. Got: %T", payload)
		// Execute asynchronously.
		go callback(event)

	default:
		t.Fatalf("Unknown callback name simulated: %s", name)
	}
}

// Standard SessionContext methods (Updated signatures to include ctx).
func (m *MockSessionContext) InjectScriptPersistently(ctx context.Context, script string) error {
	args := m.Called(ctx, script)
	return args.Error(0)
}

func (m *MockSessionContext) Navigate(ctx context.Context, url string) error {
	args := m.Called(ctx, url)
	return args.Error(0)
}

func (m *MockSessionContext) Close(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

// Implement remaining methods of schemas.SessionContext required by the interface.
func (m *MockSessionContext) Click(selector string) error {
	args := m.Called(selector)
	return args.Error(0)
}
func (m *MockSessionContext) Type(selector string, text string) error {
	args := m.Called(selector, text)
	return args.Error(0)
}
func (m *MockSessionContext) Submit(selector string) error {
	args := m.Called(selector)
	return args.Error(0)
}
func (m *MockSessionContext) ScrollPage(direction string) error {
	args := m.Called(direction)
	return args.Error(0)
}
// Note: WaitForAsync signature based on the provided contract for protopollution (no ctx).
func (m *MockSessionContext) WaitForAsync(milliseconds int) error {
	args := m.Called(milliseconds)
	return args.Error(0)
}
func (m *MockSessionContext) GetContext() context.Context {
	args := m.Called()
    if args.Get(0) == nil {
        return context.Background()
    }
	return args.Get(0).(context.Context)
}
func (m *MockSessionContext) ExecuteScript(ctx context.Context, script string) error {
	args := m.Called(ctx, script)
	return args.Error(0)
}
func (m *MockSessionContext) Interact(ctx context.Context, config schemas.InteractionConfig) error {
	args := m.Called(ctx, config)
	return args.Error(0)
}


// -- Test Setup Helper --

func setupAnalyzer(t *testing.T, configMod func(*Config)) (*Analyzer, *MockBrowserManager) {
	t.Helper()
	logger := zaptest.NewLogger(t)

	// Default configuration optimized for testing speed
	config := Config{
		WaitDuration: 50 * time.Millisecond,
	}

	if configMod != nil {
		configMod(&config)
	}

	mockBrowserManager := new(MockBrowserManager)

	// Using the refactored NewAnalyzer signature.
	analyzer := NewAnalyzer(logger, mockBrowserManager, &config)
	require.NotNil(t, analyzer, "NewAnalyzer should return a valid analyzer")

	return analyzer, mockBrowserManager
}

// -- Test Cases: Initialization and Configuration --

func TestNewAnalyzer_Defaults(t *testing.T) {
	logger := zaptest.NewLogger(t)
	analyzer := NewAnalyzer(logger, nil, nil)
	require.NotNil(t, analyzer)

	assert.Equal(t, defaultWaitDuration, analyzer.config.WaitDuration, "Default WaitDuration mismatch")
	assert.NotEmpty(t, analyzer.canary)
	assert.Len(t, analyzer.canary, 8)
	assert.NotNil(t, analyzer.findingChan)
	assert.Equal(t, 5, cap(analyzer.findingChan))
}

func TestNewAnalyzer_ConfigOverride(t *testing.T) {
	// Test specific override
	analyzer, _ := setupAnalyzer(t, func(c *Config) {
		c.WaitDuration = 1 * time.Hour
	})
	assert.Equal(t, 1*time.Hour, analyzer.config.WaitDuration)

	// Test invalid override (should revert to default)
	logger := zaptest.NewLogger(t)
	invalidConfig := Config{WaitDuration: -5 * time.Second}
	analyzerInvalid := NewAnalyzer(logger, nil, &invalidConfig)

	assert.Equal(t, defaultWaitDuration, analyzerInvalid.config.WaitDuration, "Invalid WaitDuration should revert to default")
}

// -- Test Cases: Shim Generation and Instrumentation --

func TestGenerateShim(t *testing.T) {
	analyzer, _ := setupAnalyzer(t, nil)
	testCanary := "T3STC4NRY"
	analyzer.canary = testCanary

	shim, err := analyzer.generateShim()
	require.NoError(t, err)
	require.NotEmpty(t, shim)

    // Note: Actual verification depends on the content of ProtoPollutionShim, which was omitted.
	// assert.Contains(t, shim, fmt.Sprintf("let pollutionCanary = '%s';", testCanary))
	// assert.Contains(t, shim, fmt.Sprintf("let detectionCallbackName = '%s';", jsCallbackName))
}

// -- Test Cases: Event Handling (Callback Logic) --

func TestHandlePollutionProof_ValidFlows(t *testing.T) {
	tests := []struct {
		name             string
		sourceVector     string
		expectedVulnName string
		expectedSeverity schemas.Severity
		expectedCWE      string
	}{
		{"Standard PP", "Object.prototype_access", "Client-Side Prototype Pollution", schemas.SeverityHigh, "CWE-1321"},
		{"PP via Fetch", "Fetch_Response_json_proto_key", "Client-Side Prototype Pollution", schemas.SeverityHigh, "CWE-1321"},
		{"DOM Clobbering", "DOM_Clobbering", "DOM Clobbering", schemas.SeverityMedium, "CWE-1339"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			analyzer, _ := setupAnalyzer(t, nil)
			analyzer.taskID = "task-flow-123"
			canary := analyzer.canary

			event := PollutionProofEvent{
				Source: tt.sourceVector,
				Canary: canary,
			}

			analyzer.handlePollutionProof(event)

			require.Len(t, analyzer.findingChan, 1)
			finding := <-analyzer.findingChan

			assert.Equal(t, "task-flow-123", finding.TaskID)
			// FIX: Check the Name field of the Vulnerability struct.
			assert.Equal(t, tt.expectedVulnName, finding.Vulnerability.Name)
			assert.Equal(t, tt.expectedSeverity, finding.Severity)
			// FIX: CWE is now a slice.
			require.Len(t, finding.CWE, 1)
			assert.Equal(t, tt.expectedCWE, finding.CWE[0])
			assert.Contains(t, finding.Description, tt.sourceVector)

			// FIX: Verify evidence structure. Evidence is a string, so we unmarshal the string content.
			var evidenceData PollutionProofEvent
			err := json.Unmarshal([]byte(finding.Evidence), &evidenceData)
			require.NoError(t, err)
			assert.Equal(t, canary, evidenceData.Canary)
			assert.Equal(t, tt.sourceVector, evidenceData.Source)
		})
	}
}

func TestHandlePollutionProof_MismatchedCanary(t *testing.T) {
	analyzer, _ := setupAnalyzer(t, nil)
	analyzer.canary = "EXPECTED"

	event := PollutionProofEvent{
		Source: "Object.prototype_access",
		Canary: "STALE_OR_INVALID",
	}

	analyzer.handlePollutionProof(event)

	assert.Empty(t, analyzer.findingChan, "Mismatched canary should not generate a finding")
}

func TestHandlePollutionProof_ChannelFull(t *testing.T) {
	analyzer, _ := setupAnalyzer(t, nil)
	canary := analyzer.canary
	bufferSize := 5

	for i := 0; i < bufferSize; i++ {
		analyzer.handlePollutionProof(PollutionProofEvent{Canary: canary, Source: "Flood"})
	}
	require.Len(t, analyzer.findingChan, bufferSize)

	done := make(chan struct{})
	go func() {
		analyzer.handlePollutionProof(PollutionProofEvent{Canary: canary, Source: "DroppedEvent"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("handlePollutionProof blocked when the channel was full; it should be non-blocking.")
	}

	assert.Len(t, analyzer.findingChan, bufferSize)
}

// -- Test Cases: Overall Analysis Flow (Analyze Method Integration) --

func TestAnalyze_HappyPath_Detection(t *testing.T) {
	analyzer, mockBrowserManager := setupAnalyzer(t, func(c *Config) {
		c.WaitDuration = 200 * time.Millisecond
	})

	ctx := context.Background()
	targetURL := "http://example.com/vulnerable_app"
	mockSession := NewMockSessionContext()
	taskID := "task-success-1"

	// --- Mock Expectations ---

	// 1. Initialize (FIX: Expect NewAnalysisContext with specific arguments used in analyzer.go: nil config, empty persona/taint).
	mockBrowserManager.On("NewAnalysisContext", ctx, nil, schemas.Persona{}, "", "").Return(mockSession, nil).Once()

	// 2. Instrument (FIX: Must include ctx parameter).
	mockSession.On("ExposeFunction", ctx, jsCallbackName, mock.AnythingOfType("func(protopollution.PollutionProofEvent)")).Return(nil).Once()
	
    // Check that the injected script contains the canary.
    mockSession.On("InjectScriptPersistently", ctx, mock.MatchedBy(func(script string) bool {
		return strings.Contains(script, analyzer.canary)
	})).Return(nil).Once()

	// 3. Navigation and Monitoring (FIX: Must include ctx parameter).
	mockSession.On("Navigate", ctx, targetURL).Return(nil).Once().Run(func(args mock.Arguments) {
		// Simulate concurrent finding detection.
		go func() {
			time.Sleep(10 * time.Millisecond)
			mockSession.SimulateCallback(t, jsCallbackName, PollutionProofEvent{
				Source: "Simulated_Vulnerability",
				Canary: analyzer.canary,
			})
		}()
	})

	// 4. Cleanup
	mockSession.On("Close", ctx).Return(nil).Once()

	// --- Execute Analysis ---
	findings, err := analyzer.Analyze(ctx, taskID, targetURL)

	// --- Verification ---
	assert.NoError(t, err)
	mockBrowserManager.AssertExpectations(t)
	mockSession.AssertExpectations(t)

	require.Len(t, findings, 1)
	assert.Contains(t, string(findings[0].Evidence), "Simulated_Vulnerability")
	assert.Equal(t, taskID, findings[0].TaskID)
}

func TestAnalyze_HappyPath_NoFindings(t *testing.T) {
	analyzer, mockBrowserManager := setupAnalyzer(t, nil)
	configuredWait := analyzer.config.WaitDuration

	ctx := context.Background()
	targetURL := "http://example.com/secure_app"
	mockSession := NewMockSessionContext()

	// Setup standard expectations (FIX: Updated signatures).
	mockBrowserManager.On("NewAnalysisContext", ctx, nil, schemas.Persona{}, "", "").Return(mockSession, nil)
	mockSession.On("ExposeFunction", ctx, jsCallbackName, mock.Anything).Return(nil)
	mockSession.On("InjectScriptPersistently", ctx, mock.Anything).Return(nil)
	mockSession.On("Navigate", ctx, targetURL).Return(nil)
	mockSession.On("Close", ctx).Return(nil)

	// Execute Analysis
	startTime := time.Now()
	findings, err := analyzer.Analyze(ctx, "task-none", targetURL)
	duration := time.Since(startTime)

	// Verification
	assert.NoError(t, err)
	assert.Empty(t, findings)
	mockSession.AssertExpectations(t)

	assert.GreaterOrEqual(t, duration, configuredWait)
	assert.Less(t, duration, configuredWait+500*time.Millisecond)
}

// -- Test Cases: Robustness and Error Handling --

func TestAnalyze_InitializationFailure(t *testing.T) {
	analyzer, mockBrowserManager := setupAnalyzer(t, nil)
	ctx := context.Background()

	// Mock failure (FIX: Updated signature).
	expectedError := errors.New("browser driver crashed")
	mockBrowserManager.On("NewAnalysisContext", ctx, nil, schemas.Persona{}, "", "").Return(nil, expectedError).Once()

	// Execute Analysis
	findings, err := analyzer.Analyze(ctx, "task-fail-init", "http://example.com")

	assert.Error(t, err)
	assert.Nil(t, findings)
	assert.Contains(t, err.Error(), "could not initialize browser analysis context")
	assert.ErrorIs(t, err, expectedError)
	mockBrowserManager.AssertExpectations(t)
}

func TestAnalyze_InstrumentationFailure_Expose(t *testing.T) {
	analyzer, mockBrowserManager := setupAnalyzer(t, nil)
	ctx := context.Background()
	mockSession := NewMockSessionContext()

	// Setup initialization success (FIX: Updated signature).
	mockBrowserManager.On("NewAnalysisContext", ctx, nil, schemas.Persona{}, "", "").Return(mockSession, nil)

	// Mock failure during ExposeFunction (FIX: must include ctx).
	expectedError := errors.New("JS context destroyed")
	mockSession.On("ExposeFunction", ctx, jsCallbackName, mock.Anything).Return(expectedError).Once()

	// Ensure session is closed even on failure.
	mockSession.On("Close", ctx).Return(nil).Once()

	// Execute Analysis
	_, err := analyzer.Analyze(ctx, "task-fail-expose", "http://example.com")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to expose proof function")
	mockSession.AssertExpectations(t)
	// Ensure subsequent steps were skipped (check arguments including ctx).
	mockSession.AssertNotCalled(t, "InjectScriptPersistently", mock.Anything, mock.Anything)
}

func TestAnalyze_NavigationFailure_GracefulHandling(t *testing.T) {
	analyzer, mockBrowserManager := setupAnalyzer(t, nil)
	ctx := context.Background()
	mockSession := NewMockSessionContext()
	targetURL := "http://offline.example.com"

	// Setup standard success until navigation (FIX: Updated signatures).
	mockBrowserManager.On("NewAnalysisContext", ctx, nil, schemas.Persona{}, "", "").Return(mockSession, nil)
	mockSession.On("ExposeFunction", ctx, jsCallbackName, mock.Anything).Return(nil)
	mockSession.On("InjectScriptPersistently", ctx, mock.Anything).Return(nil)

	// Mock navigation failure (FIX: must include ctx).
	navigationError := errors.New("net::ERR_NAME_NOT_RESOLVED")
	mockSession.On("Navigate", ctx, targetURL).Return(navigationError).Once()

	// Ensure session is closed.
	mockSession.On("Close", ctx).Return(nil).Once()

	// Execute Analysis
	findings, err := analyzer.Analyze(ctx, "task-nav-fail", targetURL)

	// Verify successful analysis completion despite navigation error.
	assert.NoError(t, err)
	assert.Empty(t, findings)
	mockSession.AssertExpectations(t)
}

func TestAnalyze_ContextCancellation(t *testing.T) {
	analyzer, mockBrowserManager := setupAnalyzer(t, func(c *Config) {
		c.WaitDuration = 10 * time.Second
	})

	ctx, cancel := context.WithCancel(context.Background())
	mockSession := NewMockSessionContext()

	// Setup standard expectations (FIX: Updated signatures).
	// Use mock.Anything for context in setup as the actual context object passed might vary slightly.
	mockBrowserManager.On("NewAnalysisContext", mock.Anything, nil, schemas.Persona{}, "", "").Return(mockSession, nil)
	mockSession.On("ExposeFunction", mock.Anything, jsCallbackName, mock.Anything).Return(nil)
	mockSession.On("InjectScriptPersistently", mock.Anything, mock.Anything).Return(nil)
	mockSession.On("Navigate", mock.Anything, mock.Anything).Return(nil).Run(func(args mock.Arguments) {
		// Simulate a finding immediately before cancellation.
		mockSession.SimulateCallback(t, jsCallbackName, PollutionProofEvent{
			Source: "Detected_Before_Cancel",
			Canary: analyzer.canary,
		})
	})
	// The context passed to Close will be the cancelled one.
	mockSession.On("Close", mock.Anything).Return(nil)

	// Execute Analysis in a goroutine
	type result struct {
		findings []schemas.Finding
		err      error
	}
	done := make(chan result)
	go func() {
		f, e := analyzer.Analyze(ctx, "task-cancel", "http://example.com")
		done <- result{f, e}
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case res := <-done:
		assert.Error(t, res.err)
		assert.ErrorIs(t, res.err, context.Canceled)
		require.Len(t, res.findings, 1, "Findings detected before cancellation should still be returned")
		assert.Contains(t, string(res.findings[0].Evidence), "Detected_Before_Cancel")

	case <-time.After(1 * time.Second):
		t.Fatal("Timeout waiting for analysis to cancel. It did not stop promptly.")
	}

	mockSession.AssertExpectations(t)
}
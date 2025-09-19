package protopollution

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	"github.com/xkilldash9x/scalpel-cli/internal/config"
)

// -- Mock Definitions --

// MockBrowserManager mocks the schemas.BrowserManager interface.
type MockBrowserManager struct {
	mock.Mock
}

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
	callbackFunc func(payload PollutionProofEvent)
	mu           sync.Mutex
}

func (m *MockSessionContext) ExposeFunction(ctx context.Context, name string, function interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cb, ok := function.(func(PollutionProofEvent)); ok {
		m.callbackFunc = cb
	}
	args := m.Called(ctx, name, function)
	return args.Error(0)
}

func (m *MockSessionContext) SimulateCallback(t *testing.T, functionName string, event PollutionProofEvent) {
	t.Helper()
	m.mu.Lock()
	cb := m.callbackFunc
	m.mu.Unlock()
	require.NotNil(t, cb, "Callback function was not set via ExposeFunction")
	go cb(event)
}
func (m *MockSessionContext) InjectScriptPersistently(ctx context.Context, script string) error {
	args := m.Called(ctx, script)
	return args.Error(0)
}
func (m *MockSessionContext) ExecuteScript(ctx context.Context, script string) error {
	args := m.Called(ctx, script)
	return args.Error(0)
}
func (m *MockSessionContext) Navigate(ctx context.Context, url string) error {
	args := m.Called(ctx, url)
	return args.Error(0)
}
func (m *MockSessionContext) Interact(ctx context.Context, config schemas.InteractionConfig) error {
	args := m.Called(ctx, config)
	return args.Error(0)
}
func (m *MockSessionContext) Close(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}
func (m *MockSessionContext) Click(selector string) error {
	return m.Called(selector).Error(0)
}
func (m *MockSessionContext) Type(selector string, text string) error {
	return m.Called(selector, text).Error(0)
}
func (m *MockSessionContext) Submit(selector string) error {
	return m.Called(selector).Error(0)
}
func (m *MockSessionContext) ScrollPage(direction string) error {
	return m.Called(direction).Error(0)
}
func (m *MockSessionContext) WaitForAsync(milliseconds int) error {
	return m.Called(milliseconds).Error(0)
}
func (m *MockSessionContext) GetContext() context.Context {
	args := m.Called()
	if ctx, ok := args.Get(0).(context.Context); ok {
		return ctx
	}
	return context.Background()
}
func generateCanary() string {
	return uuid.NewString()[:8]
}

// -- Test Cases --

func TestNewAnalyzer(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockBrowserManager := new(MockBrowserManager)
	cfg := config.ProtoPollutionConfig{
		Enabled: true,
	}

	analyzer := NewAnalyzer(logger, mockBrowserManager, cfg)

	assert.NotNil(t, analyzer)
	assert.Equal(t, logger.Named("pp_analyzer"), analyzer.logger)
	assert.Equal(t, mockBrowserManager, analyzer.browser)
	expectedCfg := cfg
	expectedCfg.WaitDuration = 8 * time.Second
	assert.Equal(t, expectedCfg, analyzer.config)
}

func TestAnalyze_FindingFound(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockBrowserManager := new(MockBrowserManager)
	mockSession := new(MockSessionContext)
	cfg := config.ProtoPollutionConfig{
		WaitDuration: 150 * time.Millisecond,
	}
	analyzer := NewAnalyzer(logger, mockBrowserManager, cfg)

	ctx := context.Background()
	testURL := "http://example.com/test"
	var capturedCanary string

	// --- Mocks ---
	mockBrowserManager.On("NewAnalysisContext", ctx, mock.Anything, schemas.Persona{}, "", "").Return(mockSession, nil).Once()
	mockSession.On("ExposeFunction", ctx, jsCallbackName, mock.AnythingOfType("func(protopollution.PollutionProofEvent)")).Return(nil).Once()
	mockSession.On("InjectScriptPersistently", ctx, mock.AnythingOfType("string")).Return(nil).Once().Run(func(args mock.Arguments) {
		script := args.String(1)
		re := regexp.MustCompile(`let pollutionCanary = '([^']+)';`)
		matches := re.FindStringSubmatch(script)
		require.GreaterOrEqual(t, len(matches), 2, "Could not find canary in injected script")
		capturedCanary = matches[1]
	})
	mockSession.On("Navigate", ctx, testURL).Return(nil).Once().Run(func(args mock.Arguments) {
		go func() {
			time.Sleep(20 * time.Millisecond)
			require.NotEmpty(t, capturedCanary, "Canary was not captured from injected script")
			mockSession.SimulateCallback(t, jsCallbackName, PollutionProofEvent{
				Source: "URL_SearchParams",
				Canary: capturedCanary,
				Vector: "__proto__[polluted]=true",
			})
		}()
	})
	mockSession.On("Close", ctx).Return(nil).Once()

	// --- Execute ---
	findings, err := analyzer.Analyze(ctx, "task-1", testURL)

	// --- Assertions ---
	require.NoError(t, err)
	require.Len(t, findings, 1, "Expected exactly one finding")

	finding := findings[0]
	assert.Equal(t, "Client-Side Prototype Pollution", finding.Vulnerability.Name)
	assert.Equal(t, schemas.SeverityHigh, finding.Severity)
	var evidenceData PollutionProofEvent
	err = json.Unmarshal([]byte(finding.Evidence), &evidenceData)
	require.NoError(t, err)
	assert.Equal(t, capturedCanary, evidenceData.Canary)

	mockBrowserManager.AssertExpectations(t)
	mockSession.AssertExpectations(t)
}

func TestAnalyze_NoFinding(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockBrowserManager := new(MockBrowserManager)
	mockSession := new(MockSessionContext)
	cfg := config.ProtoPollutionConfig{
		WaitDuration: 50 * time.Millisecond,
	}
	analyzer := NewAnalyzer(logger, mockBrowserManager, cfg)
	ctx := context.Background()

	mockBrowserManager.On("NewAnalysisContext", ctx, mock.Anything, schemas.Persona{}, "", "").Return(mockSession, nil)
	mockSession.On("ExposeFunction", ctx, jsCallbackName, mock.Anything).Return(nil)
	mockSession.On("InjectScriptPersistently", ctx, mock.AnythingOfType("string")).Return(nil)
	mockSession.On("Navigate", ctx, "http://clean.example.com").Return(nil)
	mockSession.On("Close", ctx).Return(nil)

	findings, err := analyzer.Analyze(ctx, "task-clean", "http://clean.example.com")

	assert.NoError(t, err)
	assert.Empty(t, findings)
	mockBrowserManager.AssertExpectations(t)
	mockSession.AssertExpectations(t)
}

func TestAnalyze_BrowserError(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mockBrowserManager := new(MockBrowserManager)
	cfg := config.ProtoPollutionConfig{}
	analyzer := NewAnalyzer(logger, mockBrowserManager, cfg)
	ctx := context.Background()
	expectedErr := errors.New("failed to launch browser")

	mockBrowserManager.On("NewAnalysisContext", ctx, mock.Anything, schemas.Persona{}, "", "").Return(nil, expectedErr)

	findings, err := analyzer.Analyze(ctx, "task-fail", "http://example.com")

	assert.Error(t, err)
	assert.ErrorIs(t, err, expectedErr)
	assert.Nil(t, findings)
	mockBrowserManager.AssertExpectations(t)
}



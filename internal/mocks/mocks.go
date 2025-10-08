// File: internal/mocks/mocks.go
package mocks

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	"github.com/xkilldash9x/scalpel-cli/internal/analysis/core"
)

// -- LLM Client Mock --

// MockLLMClient mocks the schemas.LLMClient interface.
type MockLLMClient struct {
	mock.Mock
}

// Generate provides a mock function for LLM calls.
func (m *MockLLMClient) Generate(ctx context.Context, req schemas.GenerationRequest) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	args := m.Called(ctx, req)
	return args.String(0), args.Error(1)
}

// -- Knowledge Graph Client Mock --

// MockKGClient mocks the schemas.KnowledgeGraphClient interface.
type MockKGClient struct {
	mock.Mock
}

func (m *MockKGClient) AddNode(ctx context.Context, node schemas.Node) error {
	return m.Called(ctx, node).Error(0)
}
func (m *MockKGClient) AddEdge(ctx context.Context, edge schemas.Edge) error {
	return m.Called(ctx, edge).Error(0)
}
func (m *MockKGClient) GetNode(ctx context.Context, id string) (schemas.Node, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return schemas.Node{}, args.Error(1)
	}
	return args.Get(0).(schemas.Node), args.Error(1)
}
func (m *MockKGClient) GetEdges(ctx context.Context, nodeID string) ([]schemas.Edge, error) {
	args := m.Called(ctx, nodeID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]schemas.Edge), args.Error(1)
}
func (m *MockKGClient) GetNeighbors(ctx context.Context, nodeID string) ([]schemas.Node, error) {
	args := m.Called(ctx, nodeID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]schemas.Node), args.Error(1)
}
func (m *MockKGClient) QueryImprovementHistory(ctx context.Context, goalObjective string, limit int) ([]schemas.Node, error) {
	args := m.Called(ctx, goalObjective, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]schemas.Node), args.Error(1)
}

// -- Session Context Mock --

// MockSessionContext implements the schemas.SessionContext interface for testing.
type MockSessionContext struct {
	mock.Mock
	exposedFunctions map[string]interface{}
	mutex            sync.Mutex
}

func NewMockSessionContext() *MockSessionContext {
	return &MockSessionContext{exposedFunctions: make(map[string]interface{})}
}
func (m *MockSessionContext) ExposeFunction(ctx context.Context, name string, function interface{}) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	args := m.Called(ctx, name, function)
	if args.Error(0) == nil {
		m.exposedFunctions[name] = function
	}
	return args.Error(0)
}
func (m *MockSessionContext) GetExposedFunction(name string) (interface{}, bool) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	fn, ok := m.exposedFunctions[name]
	return fn, ok
}
func (m *MockSessionContext) ID() string                      { return m.Called().String(0) }
func (m *MockSessionContext) Close(ctx context.Context) error { return m.Called(ctx).Error(0) }
func (m *MockSessionContext) Navigate(ctx context.Context, url string) error {
	return m.Called(ctx, url).Error(0)
}
func (m *MockSessionContext) Click(ctx context.Context, selector string) error {
	return m.Called(ctx, selector).Error(0)
}
func (m *MockSessionContext) Type(ctx context.Context, selector string, text string) error {
	return m.Called(ctx, selector, text).Error(0)
}
func (m *MockSessionContext) Submit(ctx context.Context, selector string) error {
	return m.Called(ctx, selector).Error(0)
}
func (m *MockSessionContext) ScrollPage(ctx context.Context, direction string) error {
	return m.Called(ctx, direction).Error(0)
}
func (m *MockSessionContext) WaitForAsync(ctx context.Context, milliseconds int) error {
	return m.Called(ctx, milliseconds).Error(0)
}
func (m *MockSessionContext) InjectScriptPersistently(ctx context.Context, script string) error {
	return m.Called(ctx, script).Error(0)
}
func (m *MockSessionContext) Interact(ctx context.Context, config schemas.InteractionConfig) error {
	return m.Called(ctx, config).Error(0)
}
func (m *MockSessionContext) Sleep(ctx context.Context, d time.Duration) error {
	return m.Called(ctx, d).Error(0)
}
func (m *MockSessionContext) DispatchMouseEvent(ctx context.Context, data schemas.MouseEventData) error {
	return m.Called(ctx, data).Error(0)
}
func (m *MockSessionContext) SendKeys(ctx context.Context, keys string) error {
	return m.Called(ctx, keys).Error(0)
}
func (m *MockSessionContext) AddFinding(ctx context.Context, finding schemas.Finding) error {
	return m.Called(ctx, finding).Error(0)
}
func (m *MockSessionContext) CollectArtifacts(ctx context.Context) (*schemas.Artifacts, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*schemas.Artifacts), args.Error(1)
}
func (m *MockSessionContext) GetElementGeometry(ctx context.Context, selector string) (*schemas.ElementGeometry, error) {
	args := m.Called(ctx, selector)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*schemas.ElementGeometry), args.Error(1)
}
func (m *MockSessionContext) ExecuteScript(ctx context.Context, script string, scriptArgs []interface{}) (json.RawMessage, error) {
	args := m.Called(ctx, script, scriptArgs)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(json.RawMessage), args.Error(1)
}

// -- Browser Manager Mock --

// MockBrowserManager mocks the schemas.BrowserManager interface.
type MockBrowserManager struct {
	mock.Mock
}

func (m *MockBrowserManager) NewAnalysisContext(sessionCtx context.Context, cfg interface{}, persona schemas.Persona, taintTemplate string, taintConfig string, findingsChan chan<- schemas.Finding) (schemas.SessionContext, error) {
	args := m.Called(sessionCtx, cfg, persona, taintTemplate, taintConfig, findingsChan)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(schemas.SessionContext), args.Error(1)
}
func (m *MockBrowserManager) Shutdown(ctx context.Context) error { return m.Called(ctx).Error(0) }

// -- OAST Provider Mock --

// MockOASTProvider mocks the OASTProvider interface.
type MockOASTProvider struct {
	mock.Mock
}

func (m *MockOASTProvider) GetInteractions(ctx context.Context, canaries []string) ([]schemas.OASTInteraction, error) {
	args := m.Called(ctx, canaries)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]schemas.OASTInteraction), args.Error(1)
}
func (m *MockOASTProvider) GetServerURL() string { return m.Called().String(0) }

// -- Analyzer Mock --

// MockAnalyzer is a mock implementation of the core.Analyzer interface.
type MockAnalyzer struct {
	mock.Mock
}

func (m *MockAnalyzer) Analyze(ctx context.Context, analysisCtx *core.AnalysisContext) error {
	return m.Called(ctx, analysisCtx).Error(0)
}
func (m *MockAnalyzer) Name() string        { return m.Called().String(0) }
func (m *MockAnalyzer) Description() string { return m.Called().String(0) }

// Type provides a mock function for returning the analyzer type.
func (m *MockAnalyzer) Type() core.AnalyzerType {
	args := m.Called()
	// Return the type configured in the mock setup, default to Unknown.
	if t, ok := args.Get(0).(core.AnalyzerType); ok {
		return t
	}
	return core.AnalyzerType(core.TypeUnknown)
}

// -- Store Mock --

// MockStore mocks the store.Store interface.
type MockStore struct {
	mock.Mock
}

// PersistData provides a mock function for persisting result envelopes.
func (m *MockStore) PersistData(ctx context.Context, data *schemas.ResultEnvelope) error {
	args := m.Called(ctx, data)
	return args.Error(0)
}

// GetFindingsByScanID provides a mock function for retrieving findings.
func (m *MockStore) GetFindingsByScanID(ctx context.Context, scanID string) ([]schemas.Finding, error) {
	args := m.Called(ctx, scanID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]schemas.Finding), args.Error(1)
}

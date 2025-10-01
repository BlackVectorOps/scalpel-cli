package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"
)

// -- BrowserExecutor Tests --

func TestBrowserExecutor_HandleNavigate(t *testing.T) {
	logger := zap.NewNop()
	mockSession := new(MockSessionContext)
	provider := func() schemas.SessionContext { return mockSession }
	executor := NewBrowserExecutor(logger, provider)

	// Test successful navigation
	action := Action{Type: ActionNavigate, Value: "https://example.com"}
	mockSession.On("Navigate", mock.Anything, "https://example.com").Return(nil).Once()
	err := executor.handleNavigate(context.Background(), mockSession, action)
	assert.NoError(t, err)

	// Test navigation failure
	expectedErr := errors.New("navigation failed")
	mockSession.On("Navigate", mock.Anything, "https://example.com").Return(expectedErr).Once()
	err = executor.handleNavigate(context.Background(), mockSession, action)
	assert.Equal(t, expectedErr, err)

	// Test missing URL
	action.Value = ""
	err = executor.handleNavigate(context.Background(), mockSession, action)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "requires a 'value'")
}

func TestBrowserExecutor_HandleSubmitForm(t *testing.T) {
	logger := zap.NewNop()
	mockSession := new(MockSessionContext)
	provider := func() schemas.SessionContext { return mockSession }
	executor := NewBrowserExecutor(logger, provider)

	action := Action{Type: ActionSubmitForm, Selector: "#form"}
	mockSession.On("Submit", mock.Anything, "#form").Return(nil).Once()
	err := executor.handleSubmitForm(context.Background(), mockSession, action)
	assert.NoError(t, err)

	action.Selector = ""
	err = executor.handleSubmitForm(context.Background(), mockSession, action)
	assert.Error(t, err)
}

func TestBrowserExecutor_HandleScroll(t *testing.T) {
	logger := zap.NewNop()
	mockSession := new(MockSessionContext)
	provider := func() schemas.SessionContext { return mockSession }
	executor := NewBrowserExecutor(logger, provider)

	action := Action{Type: ActionScroll, Value: "down"}
	mockSession.On("ScrollPage", mock.Anything, "down").Return(nil).Once()
	err := executor.handleScroll(context.Background(), mockSession, action)
	assert.NoError(t, err)

	action.Value = "up"
	mockSession.On("ScrollPage", mock.Anything, "up").Return(nil).Once()
	err = executor.handleScroll(context.Background(), mockSession, action)
	assert.NoError(t, err)

	action.Value = "" // Default to down
	mockSession.On("ScrollPage", mock.Anything, "down").Return(nil).Once()
	err = executor.handleScroll(context.Background(), mockSession, action)
	assert.NoError(t, err)
}

func TestBrowserExecutor_HandleWaitForAsync(t *testing.T) {
	logger := zap.NewNop()
	mockSession := new(MockSessionContext)
	provider := func() schemas.SessionContext { return mockSession }
	executor := NewBrowserExecutor(logger, provider)

	// Test with default duration
	action := Action{Type: ActionWaitForAsync}
	mockSession.On("WaitForAsync", mock.Anything, 1000).Return(nil).Once()
	err := executor.handleWaitForAsync(context.Background(), mockSession, action)
	assert.NoError(t, err)

	// Test with specified duration
	action.Metadata = map[string]interface{}{"duration_ms": 2500.0}
	mockSession.On("WaitForAsync", mock.Anything, 2500).Return(nil).Once()
	err = executor.handleWaitForAsync(context.Background(), mockSession, action)
	assert.NoError(t, err)

	// Test with invalid type (should use default)
	action.Metadata = map[string]interface{}{"duration_ms": "not-a-number"}
	mockSession.On("WaitForAsync", mock.Anything, 1000).Return(nil).Once()
	err = executor.handleWaitForAsync(context.Background(), mockSession, action)
	assert.NoError(t, err)
}

func TestExecutorRegistry_Execute(t *testing.T) {
	logger := zap.NewNop()
	mockSession := new(MockSessionContext)
	provider := func() schemas.SessionContext { return mockSession }
	registry := NewExecutorRegistry(logger, ".")
	registry.UpdateSessionProvider(provider)

	// Test a valid browser action
	navAction := Action{Type: ActionNavigate, Value: "https://example.com"}
	mockSession.On("Navigate", mock.Anything, "https://example.com").Return(nil).Once()
	result, err := registry.Execute(context.Background(), navAction)
	require.NoError(t, err)
	assert.Equal(t, "success", result.Status)
	assert.Equal(t, ObservedDOMChange, result.ObservationType)

	// Test an action handled by a different executor (codebase)
	// We expect an error here because we can't actually do this in a unit test without a file system.
	// But we can check that it dispatches correctly.
	codeAction := Action{Type: ActionGatherCodebaseContext, Metadata: map[string]interface{}{"module_path": "."}}
	_, err = registry.Execute(context.Background(), codeAction)
	assert.Error(t, err) // Expecting an error from the filesystem part of the executor.

	// Test an unregistered action
	unknownAction := Action{Type: "UNKNOWN_ACTION"}
	_, err = registry.Execute(context.Background(), unknownAction)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no executor registered")

	// Test a humanoid-handled action (should fail fast)
	clickAction := Action{Type: ActionClick}
	_, err = registry.Execute(context.Background(), clickAction)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "should be handled by the Agent")
}

func TestParseBrowserError(t *testing.T) {
	// Test Element Not Found
	err := errors.New("cdp: no element found for selector")
	action := Action{Selector: "#id"}
	code, details := ParseBrowserError(err, action)
	assert.Equal(t, ErrCodeElementNotFound, code)
	assert.Equal(t, "#id", details["selector"])

	// Test Timeout Error
	err = errors.New("context deadline exceeded: waiting for element timed out")
	code, details = ParseBrowserError(err, action)
	assert.Equal(t, ErrCodeTimeoutError, code)

	// Test Navigation Error
	err = errors.New("could not navigate: net::ERR_CONNECTION_REFUSED")
	code, details = ParseBrowserError(err, action)
	assert.Equal(t, ErrCodeNavigationError, code)

	// Test Geometry Error
	err = errors.New("element is not interactable (zero size)")
	code, details = ParseBrowserError(err, action)
	assert.Equal(t, ErrCodeHumanoidGeometryInvalid, code)

	// Test Generic Failure
	err = errors.New("some other random cdp error")
	code, _ = ParseBrowserError(err, action)
	assert.Equal(t, ErrCodeExecutionFailure, code)
}


package jsexec_test

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/dop251/goja_nodejs/eventloop"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	"github.com/xkilldash9x/scalpel-cli/internal/browser/jsexec"
	"go.uber.org/zap"
)

// mockBrowserEnvironment is a stub implementation for testing purposes.
type mockBrowserEnvironment struct{}

func (m *mockBrowserEnvironment) JSNavigate(targetURL string)                                     {}
func (m *mockBrowserEnvironment) NotifyURLChange(targetURL string)                                {}
func (m *mockBrowserEnvironment) ExecuteFetch(ctx context.Context, req schemas.FetchRequest) (*schemas.FetchResponse, error) {
	return nil, nil
}
func (m *mockBrowserEnvironment) AddCookieFromString(cookieStr string) error          { return nil }
func (m *mockBrowserEnvironment) GetCookieString() (string, error)                    { return "", nil }
func (m *mockBrowserEnvironment) PushHistory(state *schemas.HistoryState) error       { return nil }
func (m *mockBrowserEnvironment) ReplaceHistory(state *schemas.HistoryState) error    { return nil }
func (m *mockBrowserEnvironment) GetHistoryLength() int                               { return 0 }
func (m *mockBrowserEnvironment) GetCurrentHistoryState() interface{}                 { return nil }
func (m *mockBrowserEnvironment) ResolveURL(targetURL string) (*url.URL, error) {
	return url.Parse(targetURL)
}

// newTestRuntime is a helper to set up a runtime with mock dependencies for each test.
func newTestRuntime() *jsexec.Runtime {
	eventLoop := eventloop.NewEventLoop()
	eventLoop.Start()
	// In a real scenario, you'd stop the event loop. For these simple tests, it's okay.
	return jsexec.NewRuntime(zap.NewNop(), eventLoop, &mockBrowserEnvironment{})
}

func TestExecuteScript_Basic(t *testing.T) {
	runtime := newTestRuntime()
	ctx := context.Background()

	script := `(5 + 5) * 2`
	result, err := runtime.ExecuteScript(ctx, script, nil)

	require.NoError(t, err)
	// Goja returns numbers as int64
	assert.Equal(t, int64(20), result)
}

func TestExecuteScript_WithArgs(t *testing.T) {
	runtime := newTestRuntime()
	ctx := context.Background()

	// FIX: The script must be a function wrapper to accept arguments.
	script := `(function(prefix, message) { return prefix + message; })`
	// FIX: The arguments must be a slice, not a map.
	args := []interface{}{"Log: ", "Hello World"}

	result, err := runtime.ExecuteScript(ctx, script, args)
	require.NoError(t, err)
	assert.Equal(t, "Log: Hello World", result)
}

func TestExecuteScript_ReturnObject(t *testing.T) {
	runtime := newTestRuntime()
	ctx := context.Background()
	script := `({status: "success", code: 200})`

	result, err := runtime.ExecuteScript(ctx, script, nil)
	require.NoError(t, err)

	resMap, ok := result.(map[string]interface{})
	require.True(t, ok, "Result should be a map")
	assert.Equal(t, "success", resMap["status"])
	assert.Equal(t, int64(200), resMap["code"])
}

func TestExecuteScript_Exception(t *testing.T) {
	runtime := newTestRuntime()
	ctx := context.Background()
	script := `throw new Error("Intentional Error");`

	_, err := runtime.ExecuteScript(ctx, script, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "javascript exception:")
	assert.Contains(t, err.Error(), "Intentional Error")
}

func TestExecuteScript_Timeout(t *testing.T) {
	runtime := newTestRuntime()
	// Context with a short deadline
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Infinite loop
	script := `while(true) {}`

	startTime := time.Now()
	_, err := runtime.ExecuteScript(ctx, script, nil)
	duration := time.Since(startTime)

	require.Error(t, err)
	// FIX: The error message changed slightly in the implementation.
	assert.Contains(t, err.Error(), "javascript execution interrupted by context:")

	// Ensure it didn't run excessively long (allowing buffer)
	assert.Less(t, duration, 500*time.Millisecond)
}

func TestExecuteScript_Cancellation(t *testing.T) {
	runtime := newTestRuntime()
	ctx, cancel := context.WithCancel(context.Background())

	script := `while(true) {}`

	done := make(chan error)
	go func() {
		_, err := runtime.ExecuteScript(ctx, script, nil)
		done <- err
	}()

	// Cancel shortly after starting
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "javascript execution interrupted by context:")
		assert.Contains(t, err.Error(), context.Canceled.Error())
	case <-time.After(1 * time.Second):
		t.Fatal("Execution did not stop after cancellation")
	}
}
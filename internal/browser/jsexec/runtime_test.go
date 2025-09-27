package jsexec_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xkilldash9x/scalpel-cli/internal/browser/jsexec"
)

func TestExecuteScript_Basic(t *testing.T) {
	runtime := jsexec.NewRuntime()
	ctx := context.Background()

	script := `(5 + 5) * 2`
	result, err := runtime.ExecuteScript(ctx, script, nil)

	require.NoError(t, err)
	// Goja returns numbers as int64
	assert.Equal(t, int64(20), result)
}

func TestExecuteScript_WithArgs(t *testing.T) {
	runtime := jsexec.NewRuntime()
	ctx := context.Background()

	script := `prefix + message`
	args := map[string]interface{}{
		"prefix":  "Log: ",
		"message": "Hello World",
	}

	result, err := runtime.ExecuteScript(ctx, script, args)
	require.NoError(t, err)
	assert.Equal(t, "Log: Hello World", result)
}

func TestExecuteScript_ReturnObject(t *testing.T) {
	runtime := jsexec.NewRuntime()
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
	runtime := jsexec.NewRuntime()
	ctx := context.Background()
	script := `throw new Error("Intentional Error");`

	_, err := runtime.ExecuteScript(ctx, script, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "javascript exception:")
	assert.Contains(t, err.Error(), "Intentional Error")
}

func TestExecuteScript_Timeout(t *testing.T) {
	runtime := jsexec.NewRuntime()
	// Context with a short deadline
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Infinite loop
	script := `while(true) {}`

	startTime := time.Now()
	_, err := runtime.ExecuteScript(ctx, script, nil)
	duration := time.Since(startTime)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "javascript execution interrupted:")

	// Ensure it didn't run excessively long (allowing buffer)
	assert.Less(t, duration, 500*time.Millisecond)
}

func TestExecuteScript_Cancellation(t *testing.T) {
	runtime := jsexec.NewRuntime()
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
		assert.Contains(t, err.Error(), "javascript execution interrupted:")
		assert.Contains(t, err.Error(), context.Canceled.Error())
	case <-time.After(1 * time.Second):
		t.Fatal("Execution did not stop after cancellation")
	}
}

// browser/jsexec/runtime.go
package jsexec

import (
	"context"
	"fmt"
	"time"

	"github.com/dop251/goja"
)

// Runtime provides a sandboxed environment for executing JavaScript using Goja.
type Runtime struct{}

// NewRuntime creates a new JavaScript execution manager.
func NewRuntime() *Runtime {
	return &Runtime{}
}

const DefaultTimeout = 15 * time.Second

// ExecuteScript runs a JavaScript snippet within an isolated VM.
func (r *Runtime) ExecuteScript(ctx context.Context, script string, args map[string]interface{}) (interface{}, error) {
	// 1. Initialize a new VM for isolation.
	vm := goja.New()

	// 2. Inject arguments into the VM scope.
	if args != nil {
		for key, value := range args {
			if err := vm.Set(key, value); err != nil {
				return nil, fmt.Errorf("failed to set variable '%s': %w", key, err)
			}
		}
	}

	// 3. Determine the execution timeout.
	timeout := DefaultTimeout
	if deadline, ok := ctx.Deadline(); ok {
		timeToDeadline := time.Until(deadline)
		if timeToDeadline < timeout && timeToDeadline > 0 {
			timeout = timeToDeadline
		}
	}

	// 4. Set up timeout/cancellation handling using vm.Interrupt().
	done := make(chan struct{})
	defer close(done)

	go func() {
		select {
		case <-time.After(timeout):
			vm.Interrupt(fmt.Sprintf("Execution timeout exceeded (%v)", timeout))
		case <-ctx.Done():
			// Interrupt the VM execution upon context cancellation.
			vm.Interrupt(ctx.Err().Error())
		case <-done:
			// Execution finished normally.
		}
	}()

	// 5. Execute the script.
	result, err := vm.RunString(script)

	if err != nil {
		// Check if the error was due to the interruption.
		if _, ok := err.(*goja.InterruptedError); ok {
			return nil, fmt.Errorf("javascript execution interrupted: %w", err)
		}
		// Handle general JavaScript errors (syntax, runtime exceptions).
		if jsErr, ok := err.(*goja.Exception); ok {
			return nil, fmt.Errorf("javascript exception: %s", jsErr.String())
		}
		return nil, fmt.Errorf("javascript error: %w", err)
	}

	// 6. Export the result from the VM back to a Go type.
	return result.Export(), nil
}

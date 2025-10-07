// internal/autofix/mocks_test.go
package autofix

import (
	"context"

	"github.com/stretchr/testify/mock"
	"github.com/xkilldash9x/scalpel-cli/api/schemas"
)

// MockLLMClient is a mock implementation of schemas.LLMClient using testify/mock.
type MockLLMClient struct {
	mock.Mock
}

// Generate mocks the LLM call.
// Crucially, we implement the best practice of cooperative cancellation in the mock itself.
func (m *MockLLMClient) Generate(ctx context.Context, req schemas.GenerationRequest) (string, error) {
	// Check for cancellation immediately. This allows tests to verify the caller respects the context.
	select {
	case <-ctx.Done():
		// If the context is done (cancelled or timed out), return the context error immediately.
		return "", ctx.Err()
	default:
		// Proceed normally
	}

	args := m.Called(ctx, req)
	// Allow the mock call to simulate waiting if necessary, while still respecting context
	if args.Get(0) != nil {
		if waiter, ok := args.Get(0).(func(context.Context) (string, error)); ok {
			return waiter(ctx)
		}
	}
	
	return args.String(0), args.Error(1)
}
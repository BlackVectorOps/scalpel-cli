// internal/browser/shared_types.go
package browser

import (
	"context"
)

// SessionLifecycleObserver defines an interface for components that need to be
// notified when a session is terminated.
type SessionLifecycleObserver interface {
	unregisterSession(ac *AnalysisContext)
}


// CombineContext creates a new context derived from sessionCtx (inheriting its values,
// including the chromedp context) but ensures it is cancelled if opCtx is cancelled.
// This is crucial for allowing callers to control timeouts/cancellation (via opCtx) while still
// providing chromedp with the necessary session information (via sessionCtx).
func CombineContext(sessionCtx context.Context, opCtx context.Context) (context.Context, context.CancelFunc) {
	// Create a new context derived from the session context.
	combinedCtx, cancel := context.WithCancel(sessionCtx)

	// Monitor the operation context in a goroutine.
	// This ensures that if the operation context is cancelled, the combined context is also cancelled.
	go func() {
		select {
		case <-opCtx.Done():
			// Operation context is done (cancelled or timed out). Cancel the combined context.
			cancel()
		case <-combinedCtx.Done():
			// Combined context is done (session closed or operation completed).
		}
	}()

	return combinedCtx, cancel
}
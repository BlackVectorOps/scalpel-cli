// internal/analysis/auth/idor/types.go
package idor

import (
	"net/http"
	"sync"

	"github.com/xkilldash9x/scalpel-cli/internal/analysis/core"
)

// SessionContext holds the state and history for a single user persona.
// It's the central hub for a user's cookies, client, and observed requests.
type SessionContext struct {
	Role             string
	Client           *http.Client
	ObservedRequests map[string]ObservedRequest
	mu               sync.RWMutex // Protects ObservedRequests from concurrent access
}

// ObservedRequest is a snapshot of a request that contains potential identifiers.
// We store this to replay and modify it during the analysis phase.
type ObservedRequest struct {
	Request        *http.Request
	Body           []byte
	Identifiers    []core.ObservedIdentifier
	BaselineStatus int   // The original response status code
	BaselineLength int64 // The original response content length
}

// PredictiveJob is a work item for the predictive analysis worker.
// It bundles an observed request with a specific identifier to be tested.
type PredictiveJob struct {
	Observed   ObservedRequest
	Identifier core.ObservedIdentifier
}

package browser

// SessionLifecycleObserver allows the AnalysisContext to notify its owner (Manager) upon closure.
// This interface helps break the tight coupling between the concrete types while allowing necessary communication for Principle 4.
type SessionLifecycleObserver interface {
	unregisterSession(ac *AnalysisContext)
}
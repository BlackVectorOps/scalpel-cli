// File: internal/analysis/core/context.go
package core

import (
	"net/url"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	"github.com/xkilldash9x/scalpel-cli/internal/config"
	"go.uber.org/zap"
)

// NEW: AdapterRegistry defines the map of task types to their corresponding analyzers.
// It is populated by the worker and made available in the GlobalContext.
type AdapterRegistry map[schemas.TaskType]Analyzer

// GlobalContext holds application-wide services and configurations shared across all tasks.
type GlobalContext struct {
	Config         config.Interface
	Logger         *zap.Logger
	BrowserManager schemas.BrowserManager
	DBPool         *pgxpool.Pool
	KGClient       schemas.KnowledgeGraphClient
	OASTProvider   schemas.OASTProvider
	FindingsChan   chan<- schemas.Finding
	// NEW: Provides access to analysis adapters for dynamic invocation (e.g., by the Agent).
	Adapters AdapterRegistry
}

// AnalysisContext provides the specific context for a single analysis task.
// It includes the task details, the target, and access to the global context.
type AnalysisContext struct {
	Global    *GlobalContext
	Task      schemas.Task
	TargetURL *url.URL
	Logger    *zap.Logger
	Artifacts *schemas.Artifacts
	Findings  []schemas.Finding
	KGUpdates *schemas.KnowledgeGraphUpdate
	// NEW: Optional existing browser session.
	Session schemas.SessionContext
}

// AddFinding is a helper method to append a finding to the context.
func (ac *AnalysisContext) AddFinding(finding schemas.Finding) {
	if finding.ScanID == "" && ac.Task.ScanID != "" {
		finding.ScanID = ac.Task.ScanID
	}
	ac.Findings = append(ac.Findings, finding)
}

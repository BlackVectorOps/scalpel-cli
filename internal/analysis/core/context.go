// internal/analysis/core/context.go
package core

import (
	"net/url"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	"github.com/xkilldash9x/scalpel-cli/internal/config"
	"go.uber.org/zap"
)

// The local KnowledgeGraphClient and OASTProvider interfaces have been removed.
// They are now defined centrally in api/schemas.

// GlobalContext holds application-wide services and configurations shared across all tasks.
type GlobalContext struct {
	Config *config.Config
	Logger *zap.Logger
	// FIX: The BrowserManager field is now of type schemas.BrowserManager, the canonical interface.
	BrowserManager schemas.BrowserManager
	DBPool         *pgxpool.Pool
	// The KGClient now uses the canonical interface from schemas.
	KGClient schemas.KnowledgeGraphClient
	// The OASTProvider now uses the canonical interface from schemas.
	OASTProvider schemas.OASTProvider
	// Add other global services like HTTPClient, LLMClient, etc.
	FindingsChan     chan<- schemas.Finding 
}

// AnalysisContext provides the specific context for a single analysis task.
// It includes the task details, the target, and access to the global context.
type AnalysisContext struct {
	Global    *GlobalContext
	Task      schemas.Task
	TargetURL *url.URL
	Logger    *zap.Logger

	// Artifacts collected prior to analysis (e.g., HAR, DOM snapshot).
	// This is primarily used by passive analyzers.
	Artifacts *schemas.Artifacts

	// Findings and KGUpdates are populated by the analyzer during execution.
	Findings  []schemas.Finding
	KGUpdates *schemas.KnowledgeGraphUpdate
}

// AddFinding is a helper method to append a finding to the context.
func (ac *AnalysisContext) AddFinding(finding schemas.Finding) {
	// Ensure ScanID is populated if missing
	if finding.ScanID == "" && ac.Task.ScanID != "" {
		finding.ScanID = ac.Task.ScanID
	}
	ac.Findings = append(ac.Findings, finding)
}
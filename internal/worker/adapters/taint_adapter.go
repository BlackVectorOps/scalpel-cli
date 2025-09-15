// internal/worker/adapters/taint_adapter.go
package adapters

import (
	"context"
	"fmt"
	"time"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	"github.com/xkilldash9x/scalpel-cli/internal/analysis/active/taint"
	"github.com/xkilldash9x/scalpel-cli/internal/analysis/core"
	"github.com/xkilldash9x/scalpel-cli/internal/browser/stealth"
	"go.uber.org/zap"
)

type TaintAdapter struct {
	core.BaseAnalyzer
}

func NewTaintAdapter() *TaintAdapter {
	return &TaintAdapter{
		// FIX: Dereference pointer and add missing description/logger arguments.
		BaseAnalyzer: *core.NewBaseAnalyzer("TaintAdapter_IAST_v1", "Performs IAST analysis by tainting inputs and observing sinks.", core.TypeActive, zap.NewNop()),
	}
}

func (a *TaintAdapter) Analyze(ctx context.Context, analysisCtx *core.AnalysisContext) error {
	logger := analysisCtx.Logger.With(zap.String("adapter", a.Name()))
	logger.Info("Initializing taint analysis")

	// 1. Verify Dependencies
	if analysisCtx.Global.BrowserManager == nil {
		return fmt.Errorf("critical error: browser manager not initialized in global context")
	}

	oastProvider := analysisCtx.Global.OASTProvider
	reporter := NewContextReporter(analysisCtx)

	// 2. Configure Analyzer
	cfg := analysisCtx.Global.Config.Scanners.Active.Taint
	taintConfig := taint.Config{
		TaskID:    analysisCtx.Task.TaskID,
		Target:    analysisCtx.TargetURL,
		// FIX: Renamed from GenerateProbes/Sinks to DefaultProbes/Sinks.
		Probes:                  taint.DefaultProbes(),
		Sinks:                   taint.DefaultSinks(),
		AnalysisTimeout:         analysisCtx.Global.Config.Engine.DefaultTaskTimeout,
		EventChannelBuffer:      500,
		FinalizationGracePeriod: 5 * time.Second,
		ProbeExpirationDuration: 5 * time.Minute,
		CleanupInterval:         1 * time.Minute,
		OASTPollingInterval:     20 * time.Second,
		Interaction: schemas.InteractionConfig{ // FIX: Use the canonical schemas.InteractionConfig
			MaxDepth: cfg.Depth,
		},
	}

	// 3. Initialize Analyzer
	// FIX: The analyzer no longer takes the browser manager, only the OAST provider.
	analyzer, err := taint.NewAnalyzer(taintConfig, reporter, oastProvider, logger)
	if err != nil {
		return fmt.Errorf("failed to initialize taint analyzer: %w", err)
	}

	// 4. Create a dedicated browser session for this task.
	// This is the correct architectural pattern: the adapter manages the session lifecycle.
	session, err := analysisCtx.Global.BrowserManager.NewAnalysisContext(
		ctx,
		analysisCtx.Global.Config,
		stealth.DefaultPersona, // Using a default persona for now
		"", // No taint template needed if analyzer generates it
		"", // No taint config needed if analyzer generates it
	)
	if err != nil {
		return fmt.Errorf("failed to create browser session for taint analysis: %w", err)
	}
	defer session.Close(context.Background()) // Ensure session is always cleaned up.

	// 5. Execute Analysis with the created session.
	logger.Info("Starting taint analysis execution", zap.String("target_url", analysisCtx.TargetURL.String()))

	// This call handles the entire lifecycle: Instrument, Probe, Interact, Finalize.
	if err := analyzer.Analyze(ctx, session); err != nil {
		if ctx.Err() != nil {
			logger.Warn("Taint analysis interrupted or timed out", zap.Error(err))
			return nil // Don't treat context cancellation as a fatal task error
		}
		return fmt.Errorf("taint analysis failed during execution: %w", err)
	}

	logger.Info("Taint analysis execution completed")
	return nil
}

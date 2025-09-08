package adapters

import (
	"context"
	"fmt"
	"time" // Ensure time is imported

	"github.com/xkilldash9x/scalpel-cli/internal/analysis/active/taint"
	"github.com/xkilldash9x/scalpel-cli/internal/analysis/core"
	// "github.com/xkilldash9x/scalpel-cli/api/schemas" // Not strictly needed here if config comes from core
	"go.uber.org/zap"
)

type TaintAdapter struct {
	core.BaseAnalyzer
}

func NewTaintAdapter() *TaintAdapter {
	return &TaintAdapter{
		BaseAnalyzer: core.NewBaseAnalyzer("TaintAdapter_IAST_v1", core.TypeActive),
	}
}

func (a *TaintAdapter) Analyze(ctx context.Context, analysisCtx *core.AnalysisContext) error {
	logger := analysisCtx.Logger.With(zap.String("adapter", a.Name()))
	logger.Info("Initializing taint analysis")

	// Architectural note: The Analyzer is the boss of the browser session lifecycle.
	// The adapter must provide the dependencies (BrowserManager, OASTProvider).

	// 1. Verify Dependencies
	if analysisCtx.Global.BrowserManager == nil {
		return fmt.Errorf("critical error: browser manager not initialized in global context")
	}

	// OASTProvider may be nil if disabled, the analyzer handles this gracefully.
	oastProvider := analysisCtx.Global.OASTProvider

	reporter := NewContextReporter(analysisCtx)

	// 2. Configure Analyzer
	// Translate global config and task params into the specific Taint configuration.
	cfg := analysisCtx.Global.Config.Scanners.Active.Taint
	taintConfig := taint.Config{
		TaskID:                  analysisCtx.Task.TaskID,
		Target:                  analysisCtx.TargetURL,
		Probes:                  taint.GenerateProbes(),
		Sinks:                   taint.GenerateSinks(),
		AnalysisTimeout:         analysisCtx.Global.Config.Engine.DefaultTaskTimeout,
		EventChannelBuffer:      500,
		FinalizationGracePeriod: 5 * time.Second,
		ProbeExpirationDuration: 5 * time.Minute,
		CleanupInterval:         1 * time.Minute,
		OASTPollingInterval:     20 * time.Second, // Default or from config
		Interaction: taint.InteractionConfig{
			MaxDepth: cfg.Depth,
		},
	}

	// 3. Initialize Analyzer
	// Pass the dependencies (BrowserManager, Reporter, OASTProvider) to the Analyzer.
	analyzer, err := taint.NewAnalyzer(taintConfig, analysisCtx.Global.BrowserManager, reporter, oastProvider, logger)
	if err != nil {
		return fmt.Errorf("failed to initialize taint analyzer: %w", err)
	}

	// 4. Execute Analysis
	logger.Info("Starting taint analysis execution", zap.String("target_url", analysisCtx.TargetURL.String()))

	// This call handles the entire lifecycle: Init Session, Instrument, Probe, Interact, Finalize, Close Session.
	if err := analyzer.Analyze(ctx); err != nil {
		if ctx.Err() != nil {
			logger.Warn("Taint analysis interrupted or timed out", zap.Error(err))
			return nil // Don't treat context cancellation as a fatal task error
		}
		return fmt.Errorf("taint analysis failed during execution: %w", err)
	}

	logger.Info("Taint analysis execution completed")
	return nil
}

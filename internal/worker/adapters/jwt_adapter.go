// internal/worker/adapters/jwt_adapter.go
package adapters

import (
	"context"

	"github.com/xkilldash9x/scalpel-cli/internal/analysis/core"
	"github.com/xkilldash9x/scalpel-cli/internal/analysis/static/jwt"
	"go.uber.org/zap"
)

type JWTAdapter struct {
	core.BaseAnalyzer
}

func NewJWTAdapter() *JWTAdapter {
	return &JWTAdapter{
		// FIX: Dereference the pointer returned by NewBaseAnalyzer and provide all required arguments.
		BaseAnalyzer: *core.NewBaseAnalyzer("JWT Adapter", "Scans for common JWT vulnerabilities", core.TypeStatic, zap.NewNop()),
	}
}

func (a *JWTAdapter) Analyze(ctx context.Context, analysisCtx *core.AnalysisContext) error {
	analysisCtx.Logger.Info("Starting JWT static analysis via adapter.")

	// This is a passive analyzer, so it doesn't need task-specific parameters.
	// It will scan the HAR file provided in the AnalysisContext.

	// FIX: Instantiate the underlying analyzer correctly. It needs a logger and the config for brute-forcing.
	bruteForceEnabled := analysisCtx.Global.Config.Scanners.Static.JWT.BruteForceEnabled
	analyzer := jwt.NewJWTAnalyzer(analysisCtx.Logger, bruteForceEnabled)

	// FIX: The adapter's role is to delegate the analysis. The analyzer itself
	// will add any findings to the analysisCtx.
	return analyzer.Analyze(ctx, analysisCtx)
}

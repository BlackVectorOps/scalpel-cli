package adapters

import (
	"context"
	"fmt"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	"github.com/xkilldash9x/scalpel-cli/internal/agent"
	"github.com/xkilldash9x/scalpel-cli/internal/analysis/core"
	"go.uber.org/zap"
)

// the bridge that lets our autonomous agent play nice as a standard worker module.
type AgentAdapter struct {
	*core.BaseAnalyzer
}

//  creates a new adapter for agent missions.
func NewAgentAdapter() *AgentAdapter {
	return &AgentAdapter{
		BaseAnalyzer: core.NewBaseAnalyzer("AgentAdapter", "Executes autonomous agent missions", core.TypeAgent, zap.NewNop()),
	}
}

// kicks off an agent mission.
func (a *AgentAdapter) Analyze(ctx context.Context, analysisCtx *core.AnalysisContext) error {
	a.Logger = analysisCtx.Logger
	a.Logger.Info("Agent mission received. Initializing agent...")

	// Use strongly typed parameters for robustness and clarity.
	// FIX: Use robust type assertion to handle both value and pointer types.
	var params schemas.AgentMissionParams
	switch p := analysisCtx.Task.Parameters.(type) {
	case schemas.AgentMissionParams:
		params = p
	case *schemas.AgentMissionParams:
		if p == nil {
			return fmt.Errorf("invalid parameters: nil pointer for Agent mission")
		}
		params = *p
	default:
		actualType := fmt.Sprintf("%T", analysisCtx.Task.Parameters)
		a.Logger.Error("Invalid parameter type assertion for Agent mission",
			zap.String("expected", "schemas.AgentMissionParams or pointer"),
			zap.String("actual", actualType))
		return fmt.Errorf("invalid parameters type for Agent mission; expected schemas.AgentMissionParams or *schemas.AgentMissionParams, got %s", actualType)
	}

	// Validate required parameters.
	if params.MissionBrief == "" {
		return fmt.Errorf("validation error: agent mission task is missing required 'MissionBrief'")
	}

	// The agent needs access to the global context to use shared services
	// like the logger, browser manager, and knowledge graph.
	agentMission := agent.Mission{
		ID:        analysisCtx.Task.TaskID,
		Objective: params.MissionBrief,
		TargetURL: analysisCtx.Task.TargetURL,
	}

	// Initialize the agent instance, passing the full global context.
	agentInstance, err := agent.New(ctx, agentMission, analysisCtx.Global)
	if err != nil {
		return fmt.Errorf("failed to initialize agent: %w", err)
	}

	a.Logger.Info("Agent initialized. Starting mission execution.")

	// Run the agent's main logic loop.
	// This is a blocking call that will run until the mission is complete or the context is cancelled.
	missionResult, err := agentInstance.RunMission(ctx)
	if err != nil {
		// Check if the error was due to context cancellation (timeout or interruption).
		if ctx.Err() != nil {
			a.Logger.Warn("Agent mission interrupted or timed out.", zap.Error(err))
			return ctx.Err()
		}
		return fmt.Errorf("agent mission failed: %w", err)
	}

	// Integrate the results from the agent back into the analysis context.
	a.Logger.Info("Agent mission completed.", zap.String("summary", missionResult.Summary))

	// Merge findings and Knowledge Graph updates.
	if len(missionResult.Findings) > 0 {
		for _, finding := range missionResult.Findings {
			analysisCtx.AddFinding(finding)
		}
	}

	if missionResult.KGUpdates != nil {
		analysisCtx.KGUpdates.NodesToAdd = append(analysisCtx.KGUpdates.NodesToAdd, missionResult.KGUpdates.NodesToAdd...)
		analysisCtx.KGUpdates.EdgesToAdd = append(analysisCtx.KGUpdates.EdgesToAdd, missionResult.KGUpdates.EdgesToAdd...)
	}

	return nil
}

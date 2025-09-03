// File: pkg/agent/agent.go

package agent

import (
	"context"
	"fmt"
	"time"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"github.com/xkilldash9x/scalpel-cli/pkg/analysis/core"
	"github.com/xkilldash9x/scalpel-cli/pkg/browser"
	"github.com/xkilldash9x/scalpel-cli/pkg/config"
	"github.com/xkilldash9x/scalpel-cli/pkg/knowledgegraph"
	"github.com/xkilldash9x/scalpel-cli/pkg/llmclient"
	"github.com/xkilldash9x/scalpel-cli/pkg/schemas"
)

// Agent is the autonomous unit responsible for executing security analysis missions.
type Agent struct {
	ID                  string
	cfg                 *config.Config
	logger              *zap.Logger
	globalCtx           *core.GlobalContext // Shared resources
	Mind                Mind
	CognitiveBus        *CognitiveBus
	KnowledgeGraph      knowledgegraph.GraphStore
	Executors           map[ActionType]ActionExecutor
	BrowserInteractor   browser.BrowserInteractor // Corrected type name
	CurrentSession      browser.SessionContext
	CurrentMission      Mission
	missionCompleteChan chan error
}

// New initializes a new autonomous agent.
func New(ctx context.Context, agentCfg config.AgentConfig, globalCtx *core.GlobalContext) (*Agent, error) {
	agentID := fmt.Sprintf("agent-%s", uuid.New().String()[:8])
	agentLogger := globalCtx.Logger.Named("agent").With(zap.String("agent_id", agentID))

	kg := knowledgegraph.New(agentLogger)
	bus := NewCognitiveBus(agentLogger, 100)

	client, err := llmclient.NewClient(agentCfg, agentLogger)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize LLM Client: %w", err)
	}

	//  mind for the agent
	mind := NewLLMMind(agentLogger, client, agentCfg, kg, bus)

	agent := &Agent{
		ID:                  agentID,
		cfg:                 globalCtx.Config,
		logger:              agentLogger,
		globalCtx:           globalCtx,
		Mind:                mind,
		CognitiveBus:        bus,
		KnowledgeGraph:      kg,
		BrowserInteractor:   globalCtx.BrowserManager, // Corrected field name
		missionCompleteChan: make(chan error, 1),
		Executors:           make(map[ActionType]ActionExecutor),
	}

	agent.registerExecutors()
	return agent, nil
}

//initializes and registers the action executors.
func (a *Agent) registerExecutors() {
	browserExec := NewBrowserExecutor(a.logger, func() browser.SessionContext {
		return a.CurrentSession
	})
	a.Executors[ActionNavigate] = browserExec
	a.Executors[ActionClick] = browserExec
	a.Executors[ActionInputText] = browserExec
	a.Executors[ActionSubmitForm] = browserExec
	a.Executors[ActionScroll] = browserExec
	a.Executors[ActionWaitForAsync] = browserExec
	a.Executors[ActionConclude] = NewMissionControlExecutor(a) // Conclude is a mission action
}

// primary entry point for executing a mission.
func (a *Agent) RunMission(ctx context.Context) (schemas.MissionResult, error) {
	mission := a.Mind.(*LLMMind).currentMission
	a.logger.Info("Starting new mission", zap.String("mission_id", mission.ID), zap.String("objective", mission.Objective))

	if mission.TargetURL != "" {
		session, err := a.BrowserInteractor.InitializeSession(ctx)
		if err != nil {
			return schemas.MissionResult{}, fmt.Errorf("failed to initialize browser session for mission: %w", err)
		}
		a.CurrentSession = session
		defer a.cleanupSession()
	}

	// Start the main loops
	go a.Mind.Start(ctx)
	go a.runExecutionLoop(ctx)

	// Block until the mission completes or context is cancelled.
	select {
	case <-ctx.Done():
		a.logger.Warn("Mission timed out or was cancelled externally", zap.Error(ctx.Err()))
		return schemas.MissionResult{}, ctx.Err()
	case err := <-a.missionCompleteChan:
		if err != nil {
			return schemas.MissionResult{}, err
		}
		return a.summarizeMissionResult(), nil
	}
}

//listens to the bus and executes actions from the Mind.
func (a *Agent) runExecutionLoop(ctx context.Context) {
	actionChan, unsubscribe := a.CognitiveBus.Subscribe(MessageTypeAction)
	defer unsubscribe()

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-actionChan:
			if !ok {
				return
			}
			if action, ok := msg.Payload.(Action); ok {
				a.executeAction(ctx, action)
			}
			a.CognitiveBus.Acknowledge(msg)
		}
	}
}

// finds and uses the correct executor for an action.
func (a *Agent) executeAction(ctx context.Context, action Action) {
	executor, exists := a.Executors[action.Type]
	if !exists {
		a.logger.Error("No executor registered for action type", zap.String("type", string(action.Type)))
		return
	}

	result, err := executor.Execute(ctx, action)
	if err != nil {
		a.logger.Error("Action pre-flight check failed", zap.Error(err), zap.String("action_id", action.ID))
		// Report pre flight failure as an observation
		a.postObservation(action.ID, &ExecutionResult{
			Status:          "failed",
			Error:           err.Error(),
			ObservationType: ObservedSystemState,
		})
		return
	}
	// Post the result of the execution attempt.
	a.postObservation(action.ID, result)
}

//  sends the outcome of an action back to the bus for the Mind to observe.
func (a *Agent) postObservation(sourceActionID string, result *ExecutionResult) {
	obs := Observation{
		ID:             uuid.NewString(),
		MissionID:      a.Mind.(*LLMMind).currentMission.ID,
		SourceActionID: sourceActionID,
		Type:           result.ObservationType,
		Data:           result,
		Timestamp:      time.Now().UTC(),
	}

	if err := a.CognitiveBus.Post(context.Background(), CognitiveMessage{Type: MessageTypeObservation, Payload: obs}); err != nil {
		a.logger.Error("Failed to post observation to bus", zap.Error(err))
	}
}

//  ensures the browser session is closed.
func (a *Agent) cleanupSession() {
	if a.CurrentSession != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.CurrentSession.Close(cleanupCtx); err != nil {
			a.logger.Warn("Error closing browser session", zap.Error(err))
		}
		a.CurrentSession = nil
	}
}

//  collects the final state from the knowledge graph.
func (a *Agent) summarizeMissionResult() schemas.MissionResult {
	// In a real implementation, this would query the KG for findings and generate a summary.
	return schemas.MissionResult{
		Summary: "Mission concluded.",
	}
}

//  returns an executor for meta-actions like concluding a mission.
func NewMissionControlExecutor(agent *Agent) ActionExecutor {
	return &missionControlExecutor{agent: agent}
}

type missionControlExecutor struct {
	agent *Agent
}

func (e *missionControlExecutor) Execute(ctx context.Context, action Action) (*ExecutionResult, error) {
	if action.Type == ActionConclude {
		e.agent.missionCompleteChan <- nil // Signal success
		return &ExecutionResult{Status: "success", ObservationType: ObservedSystemState}, nil
	}
	return nil, fmt.Errorf("unsupported mission control action: %s", action.Type)
}

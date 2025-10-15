// File: agent/agent_test.go
package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	"github.com/xkilldash9x/scalpel-cli/internal/browser/humanoid"
	"github.com/xkilldash9x/scalpel-cli/internal/mocks"
)

// -- Local Mocks to prevent import cycles --
// These are correct because Mind and ExecutorRegistry are in the 'agent' package.

// MockMind mocks the agent.Mind interface.
type MockMind struct {
	mock.Mock
}

func (m *MockMind) Start(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}
func (m *MockMind) Stop() {
	m.Called()
}
func (m *MockMind) SetMission(mission Mission) {
	m.Called(mission)
}

// MockExecutorRegistry mocks the agent.ActionRegistry.
type MockExecutorRegistry struct {
	mock.Mock
}

func (m *MockExecutorRegistry) Execute(ctx context.Context, action Action) (*ExecutionResult, error) {
	args := m.Called(ctx, action)
	if result, ok := args.Get(0).(*ExecutionResult); ok {
		return result, args.Error(1)
	}
	return nil, args.Error(1)
}

// UpdateSessionProvider satisfies the ActionRegistry interface with the correct type.
func (m *MockExecutorRegistry) UpdateSessionProvider(provider SessionProvider) {
	m.Called(provider)
}

// setupAgentTest initializes a complete agent with mocked dependencies.
func setupAgentTest(t *testing.T) (*Agent, *MockMind, *CognitiveBus, *MockExecutorRegistry, *mocks.MockHumanoidController, *mocks.MockKGClient, *mocks.MockLLMClient) {
	t.Helper()
	logger := zaptest.NewLogger(t)
	mission := Mission{ID: "test-mission", Objective: "test-objective"}

	// Mocks for all major components
	mockMind := new(MockMind)
	bus := NewCognitiveBus(logger, 50)
	mockExecutors := new(MockExecutorRegistry)
	mockHumanoid := new(mocks.MockHumanoidController)
	mockKG := new(mocks.MockKGClient)
	mockLLM := new(mocks.MockLLMClient)

	agent := &Agent{
		mission:            mission,
		logger:             logger,
		mind:               mockMind,
		bus:                bus,
		executors:          mockExecutors,
		humanoid:           mockHumanoid,
		kg:                 mockKG,
		llmClient:          mockLLM,
		resultChan:         make(chan MissionResult, 1),
		actionDispatchChan: make(chan CognitiveMessage, 50),
		// missionWg is implicitly initialized to zero, which is fine for tests
		// that don't call the full RunMission.
	}

	t.Cleanup(func() {
		bus.Shutdown()
	})

	return agent, mockMind, bus, mockExecutors, mockHumanoid, mockKG, mockLLM
}

// TestAgent_RunMission_Success verifies the happy path of a mission execution.
func TestAgent_RunMission_Success(t *testing.T) {
	agent, mockMind, _, _, _, _, _ := setupAgentTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	mockMind.On("SetMission", agent.mission).Return().Once()
	mockMind.On("Start", mock.Anything).Return(nil).Once()
	mockMind.On("Stop").Return().Once()

	expectedResult := MissionResult{Summary: "Mission accomplished"}
	go func() {
		time.Sleep(50 * time.Millisecond)
		agent.finish(expectedResult)
	}()

	result, err := agent.RunMission(ctx)

	require.NoError(t, err)
	assert.Equal(t, &expectedResult, result)
	mockMind.AssertExpectations(t)
}

// TestAgent_RunMission_MindFailure verifies the agent fails fast if the Mind fails to start.
func TestAgent_RunMission_MindFailure(t *testing.T) {
	// Arrange
	agent, mockMind, _, _, _, _, _ := setupAgentTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	mindError := errors.New("cognitive failure")

	mockMind.On("SetMission", agent.mission).Return().Once()
	mockMind.On("Start", mock.Anything).Return(mindError).Once()

	// Act: Run the mission. We expect it to fail immediately.
	result, err := agent.RunMission(ctx)

	// Assert: The error from mind.Start should be propagated.
	require.Error(t, err, "Expected an error because the mind failed to start")
	assert.ErrorIs(t, err, mindError, "The specific error from the mind should be wrapped and returned")
	assert.Nil(t, result, "Result should be nil on a startup failure")

	// Verify that all expected mock calls were made.
	mockMind.AssertExpectations(t)
}

// TestAgent_ActionLoop verifies the correct dispatching of various action types.
func TestAgent_ActionLoop(t *testing.T) {

	t.Run("ConcludeAction", func(t *testing.T) {
		rootCtx, cancelRoot := context.WithCancel(context.Background())
		defer cancelRoot()
		agent, mockMind, _, _, _, mockKG, mockLLM := setupAgentTest(t)

		// The agent's finish method will call Stop, so we need to expect it.
		mockMind.On("Stop").Return()

		// Run workers in the background.
		go agent.startActionWorkers(rootCtx, 1)

		mockKG.On("GetNode", mock.Anything, agent.mission.ID).Return(schemas.Node{}, nil).Once()
		mockKG.On("GetEdges", mock.Anything, agent.mission.ID).Return(nil, nil).Once()
		mockLLM.On("Generate", mock.Anything, mock.Anything).Return("Mission concluded.", nil).Once()

		action := Action{Type: ActionConclude, Rationale: "Finished"}
		agent.actionDispatchChan <- CognitiveMessage{Type: MessageTypeAction, Payload: action}

		select {
		case result := <-agent.resultChan:
			assert.Equal(t, "Mission concluded.", result.Summary)
		case <-time.After(2 * time.Second):
			t.Fatal("Timeout waiting for conclusion")
		}

		// Clean shutdown.
		close(agent.actionDispatchChan)
		agent.missionWg.Wait()
		mockMind.AssertExpectations(t)
	})

	t.Run("HumanoidAction", func(t *testing.T) {
		rootCtx, cancelRoot := context.WithCancel(context.Background())
		defer cancelRoot()

		agent, _, bus, _, mockHumanoid, _, _ := setupAgentTest(t)

		agent.missionWg.Add(1)
		agent.startActionWorkers(rootCtx, 1)
		agent.missionWg.Add(1)
		go agent.actionDispatcherLoop(rootCtx)

		mockHumanoid.On("IntelligentClick", mock.Anything, "#button", (*humanoid.InteractionOptions)(nil)).Return(nil).Once()

		action := Action{Type: ActionClick, Selector: "#button"}
		bus.Post(context.Background(), CognitiveMessage{Type: MessageTypeAction, Payload: action})

		time.Sleep(50 * time.Millisecond)
		mockHumanoid.AssertExpectations(t)
		close(agent.actionDispatchChan)
		agent.missionWg.Wait()
	})

	t.Run("ExecutorRegistryAction", func(t *testing.T) {
		rootCtx, cancelRoot := context.WithCancel(context.Background())
		defer cancelRoot()
		agent, _, bus, mockExecutors, _, _, _ := setupAgentTest(t)

		agent.missionWg.Add(1)
		agent.startActionWorkers(rootCtx, 1)
		agent.missionWg.Add(1)
		go agent.actionDispatcherLoop(rootCtx)

		action := Action{Type: ActionNavigate, Value: "http://test.com"}
		execResult := &ExecutionResult{Status: "success"}
		mockExecutors.On("Execute", mock.Anything, action).Return(execResult, nil).Once()

		bus.Post(context.Background(), CognitiveMessage{Type: MessageTypeAction, Payload: action})

		time.Sleep(50 * time.Millisecond)
		mockExecutors.AssertExpectations(t)
		close(agent.actionDispatchChan)
		agent.missionWg.Wait()
	})

	t.Run("UnknownAction", func(t *testing.T) {
		rootCtx, cancelRoot := context.WithCancel(context.Background())
		defer cancelRoot()
		agent, _, bus, _, _, _, _ := setupAgentTest(t)
		observationChan, unsub := bus.Subscribe(MessageTypeObservation)
		defer unsub()

		agent.missionWg.Add(1)
		agent.startActionWorkers(rootCtx, 1)
		agent.missionWg.Add(1)
		go agent.actionDispatcherLoop(rootCtx)

		action := Action{Type: "UNKNOWN_ACTION"}
		bus.Post(context.Background(), CognitiveMessage{Type: MessageTypeAction, Payload: action})

		select {
		case msg := <-observationChan:
			obs, ok := msg.Payload.(Observation)
			require.True(t, ok)
			assert.Equal(t, "failed", obs.Result.Status)
			assert.Equal(t, ErrCodeUnknownAction, obs.Result.ErrorCode)
			bus.Acknowledge(msg)
		case <-time.After(2 * time.Second):
			t.Fatal("Timeout waiting for observation of unknown action")
		}
		close(agent.actionDispatchChan)
		agent.missionWg.Wait()
	})

	t.Run("PanicInExecutor", func(t *testing.T) {
		rootCtx, cancelRoot := context.WithCancel(context.Background())
		defer cancelRoot()
		agent, _, bus, mockExecutors, _, _, _ := setupAgentTest(t)
		observationChan, unsub := bus.Subscribe(MessageTypeObservation)
		defer unsub()

		agent.missionWg.Add(1)
		agent.startActionWorkers(rootCtx, 1)
		agent.missionWg.Add(1)
		go agent.actionDispatcherLoop(rootCtx)

		action := Action{Type: ActionNavigate, Value: "http://test.com"}

		// Setup the mock to panic when called
		mockExecutors.On("Execute", mock.Anything, action).Run(func(args mock.Arguments) {
			panic("executor exploded")
		}).Return(nil, nil).Once()

		bus.Post(context.Background(), CognitiveMessage{Type: MessageTypeAction, Payload: action})

		select {
		case msg := <-observationChan:
			obs, ok := msg.Payload.(Observation)
			require.True(t, ok)
			assert.Equal(t, "failed", obs.Result.Status)
			assert.Equal(t, ErrCodeExecutorPanic, obs.Result.ErrorCode)
			assert.Contains(t, obs.Result.ErrorDetails["message"], "panic: executor exploded")
			bus.Acknowledge(msg)
		case <-time.After(2 * time.Second):
			t.Fatal("Timeout waiting for panic observation")
		}
		close(agent.actionDispatchChan)
		agent.missionWg.Wait()
	})
}

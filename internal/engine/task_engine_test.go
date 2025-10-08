// internal/engine/engine_test.go
package engine

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	"github.com/xkilldash9x/scalpel-cli/internal/analysis/core"
	"github.com/xkilldash9x/scalpel-cli/internal/config"
	"github.com/xkilldash9x/scalpel-cli/internal/mocks"
)

// -- Mock Implementations --

// mockWorker simulates the behavior of the MonolithicWorker.
type mockWorker struct {
	// A function that can be customized per test to simulate different outcomes.
	processFunc func(ctx context.Context, analysisCtx *core.AnalysisContext) error
}

func (m *mockWorker) ProcessTask(ctx context.Context, analysisCtx *core.AnalysisContext) error {
	if m.processFunc != nil {
		return m.processFunc(ctx, analysisCtx)
	}
	// Default behavior: do nothing and succeed.
	return nil
}

// -- Test Suite --

// TestTaskEngine_StartStop verifies the engine's core lifecycle: starting, processing tasks, and stopping gracefully.
func TestTaskEngine_StartStop(t *testing.T) {
	// -- Setup --
	cfg := &config.Config{
		Engine: config.EngineConfig{
			WorkerConcurrency:  2,
			DefaultTaskTimeout: 5 * time.Second,
		},
	}
	logger := zap.NewNop()
	store := new(mocks.MockStore)

	// This mock worker will add a finding to each task it processes.
	worker := &mockWorker{
		processFunc: func(ctx context.Context, analysisCtx *core.AnalysisContext) error {
			analysisCtx.Findings = append(analysisCtx.Findings, schemas.Finding{ID: "finding-" + analysisCtx.Task.TaskID})
			return nil
		},
	}

	engine, err := New(cfg, logger, store, new(mocks.MockBrowserManager), new(mocks.MockKGClient))
	require.NoError(t, err)

	// We need to replace the real worker with our mock.
	engine.worker = worker

	// -- Execution --
	taskChan := make(chan schemas.Task, 10)

	// Expect PersistData to be called for each successful task with findings.
	numTasks := 3
	store.On("PersistData", mock.Anything, mock.Anything).Return(nil).Times(numTasks)

	engine.Start(context.Background(), taskChan)

	for i := 0; i < numTasks; i++ {
		taskChan <- schemas.Task{TaskID: fmt.Sprintf("task-%d", i), TargetURL: "https://example.com"}
	}
	close(taskChan) // Closing the channel signals the engine to shut down its workers.

	// Wait for the engine to stop gracefully.
	engine.Stop()

	// -- Assertions --
	store.AssertExpectations(t)
}

// TestTaskEngine_WorkerError verifies that if a worker returns an error, the result is not persisted.
func TestTaskEngine_WorkerError(t *testing.T) {
	// -- Setup --
	cfg := &config.Config{Engine: config.EngineConfig{WorkerConcurrency: 1}}
	logger := zap.NewNop()
	store := new(mocks.MockStore)
	worker := &mockWorker{
		processFunc: func(ctx context.Context, analysisCtx *core.AnalysisContext) error {
			// Simulate a processing error.
			return errors.New("worker failed spectacularly")
		},
	}

	engine, err := New(cfg, logger, store, new(mocks.MockBrowserManager), new(mocks.MockKGClient))
	require.NoError(t, err)
	engine.worker = worker

	// -- Execution --
	taskChan := make(chan schemas.Task, 1)
	engine.Start(context.Background(), taskChan)

	taskChan <- schemas.Task{TaskID: "task-fail", TargetURL: "https://example.com"}
	close(taskChan)
	engine.Stop()

	// -- Assertions --
	store.AssertNotCalled(t, "PersistData", mock.Anything, mock.Anything)
}

// TestTaskEngine_NoResults verifies that no data is persisted if a task yields no findings or KG updates.
func TestTaskEngine_NoResults(t *testing.T) {
	// -- Setup --
	cfg := &config.Config{Engine: config.EngineConfig{WorkerConcurrency: 1}}
	logger := zap.NewNop()
	store := new(mocks.MockStore)
	worker := &mockWorker{
		// Default processFunc returns success with no findings.
	}

	engine, err := New(cfg, logger, store, new(mocks.MockBrowserManager), new(mocks.MockKGClient))
	require.NoError(t, err)
	engine.worker = worker

	// -- Execution --
	taskChan := make(chan schemas.Task, 1)
	engine.Start(context.Background(), taskChan)

	taskChan <- schemas.Task{TaskID: "task-no-findings", TargetURL: "https://example.com"}
	close(taskChan)
	engine.Stop()

	// -- Assertions --
	store.AssertNotCalled(t, "PersistData", mock.Anything, mock.Anything)
}

// TestTaskEngine_ContextCancellation ensures workers shut down when the main context is cancelled.
func TestTaskEngine_ContextCancellation(t *testing.T) {
	// -- Setup --
	cfg := &config.Config{Engine: config.EngineConfig{WorkerConcurrency: 2}}
	logger := zap.NewNop()
	store := new(mocks.MockStore)

	// This worker will block until its context is cancelled.
	worker := &mockWorker{
		processFunc: func(ctx context.Context, analysisCtx *core.AnalysisContext) error {
			<-ctx.Done() // Wait for cancellation
			return ctx.Err()
		},
	}

	engine, err := New(cfg, logger, store, new(mocks.MockBrowserManager), new(mocks.MockKGClient))
	require.NoError(t, err)
	engine.worker = worker

	// -- Execution --
	ctx, cancel := context.WithCancel(context.Background())
	taskChan := make(chan schemas.Task, 2)
	engine.Start(ctx, taskChan)

	// Send tasks to get the workers busy.
	taskChan <- schemas.Task{TaskID: "task-1", TargetURL: "https://example.com"}
	taskChan <- schemas.Task{TaskID: "task-2", TargetURL: "https://example.com"}

	// Give workers a moment to start processing.
	time.Sleep(100 * time.Millisecond)

	// Cancel the context and then try to stop the engine.
	cancel()
	engine.Stop() // This should return quickly because the workers respected the cancellation.

	// -- Assertions --
	// The main assertion is that the Stop() call completes without a timeout.
	// We also expect no data to be persisted because the tasks were cancelled.
	store.AssertNotCalled(t, "PersistData", mock.Anything, mock.Anything)
}

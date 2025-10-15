// File: cmd/factory.go

package cmd

import (
	"context"

	"fmt"

	"sync"

	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"

	"github.com/xkilldash9x/scalpel-cli/internal/analysis/core"

	"github.com/xkilldash9x/scalpel-cli/internal/browser"

	"github.com/xkilldash9x/scalpel-cli/internal/browser/network"

	"github.com/xkilldash9x/scalpel-cli/internal/config"

	"github.com/xkilldash9x/scalpel-cli/internal/discovery"

	"github.com/xkilldash9x/scalpel-cli/internal/engine"

	"github.com/xkilldash9x/scalpel-cli/internal/knowledgegraph"

	"github.com/xkilldash9x/scalpel-cli/internal/observability"

	"github.com/xkilldash9x/scalpel-cli/internal/orchestrator"

	"github.com/xkilldash9x/scalpel-cli/internal/store"

	"github.com/xkilldash9x/scalpel-cli/internal/worker"

	"go.uber.org/zap"
)

// Components holds all the initialized services required for a scan.

// This struct centralizes the lifecycle management of scan-related dependencies.

type Components struct {
	Store schemas.Store

	BrowserManager schemas.BrowserManager

	KnowledgeGraph schemas.KnowledgeGraphClient

	TaskEngine schemas.TaskEngine

	DiscoveryEngine schemas.DiscoveryEngine

	Orchestrator schemas.Orchestrator

	DBPool *pgxpool.Pool

	// findingsChan is used to decouple finding generation from persistence.

	findingsChan chan schemas.Finding

	// consumerWG is used to ensure the findings consumer has finished draining the channel.

	consumerWG *sync.WaitGroup
}

// Shutdown gracefully closes all components, ensuring resources are released in the correct order.

func (c *Components) Shutdown() {

	logger := observability.GetLogger()

	logger.Debug("Beginning components shutdown sequence.")

	// 1. Stop the engines first (Producers) to cease generating new work.

	if c.TaskEngine != nil {

		c.TaskEngine.Stop()

		logger.Debug("Task engine stopped.")

	}

	if c.DiscoveryEngine != nil {

		c.DiscoveryEngine.Stop()

		logger.Debug("Discovery engine stopped.")

	}

	// 2. Close the findings channel. This signals the consumer to drain and stop.

	if c.findingsChan != nil {

		close(c.findingsChan)

		logger.Debug("Findings channel closed.")

	}

	// 3. Wait for the consumer to finish processing the drained channel.

	if c.consumerWG != nil {

		c.consumerWG.Wait()

		logger.Debug("Findings consumer finished processing.")

	}

	// 4. Shut down the browser manager.

	if c.BrowserManager != nil {

		// Use a separate context with a timeout for shutdown to ensure it completes

		// even if the main application context was canceled.

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

		defer cancel()

		if err := c.BrowserManager.Shutdown(shutdownCtx); err != nil {

			logger.Warn("Error during browser manager shutdown.", zap.Error(err))

		} else {

			logger.Debug("Browser manager shut down.")

		}

	}

	// 5. Close the database connection pool.

	if c.DBPool != nil {

		c.DBPool.Close()

		logger.Debug("Database connection pool closed.")

	}

	logger.Info("All scan components shut down successfully.")

}

// ComponentFactory defines the interface for creating the set of components needed for a scan.

// This abstraction is the key to making the scan command's logic testable.

// Returns interface{} to avoid import cycles with the mocks package.

type ComponentFactory interface {
	Create(ctx context.Context, cfg config.Interface, targets []string) (interface{}, error)
}

// concreteFactory is the production implementation of the ComponentFactory.

type concreteFactory struct{}

// NewComponentFactory creates a new production-ready component factory.

func NewComponentFactory() ComponentFactory {

	return &concreteFactory{}

}

// Create handles the full dependency injection and initialization of scan components.

func (f *concreteFactory) Create(ctx context.Context, cfg config.Interface, targets []string) (interface{}, error) {

	logger := observability.GetLogger()

	components := &Components{

		// Initialize the findings channel early with a generous buffer.

		findingsChan: make(chan schemas.Finding, 1024),

		// Initialize the WaitGroup as a pointer to avoid copy issues.

		consumerWG: &sync.WaitGroup{},
	}

	// STABILITY ENHANCEMENT: Ensure cleanup happens if initialization fails midway.

	var initializationErr error

	defer func() {

		if initializationErr != nil {

			logger.Warn("Initialization failed, shutting down partially created components.", zap.Error(initializationErr))

			// Call Shutdown on the partially initialized components struct.

			components.Shutdown()

		}

	}()

	// 1. Database Pool

	if cfg.Database().URL == "" {

		initializationErr = fmt.Errorf("database URL is not configured (hint: check SCALPEL_DATABASE_URL)")

		return nil, initializationErr

	}

	dbPool, err := pgxpool.New(ctx, cfg.Database().URL)

	if err != nil {

		initializationErr = fmt.Errorf("failed to create database connection pool: %w", err)

		return nil, initializationErr

	}

	// Add to components immediately so the deferred Shutdown can close it if later steps fail.

	components.DBPool = dbPool

	if err := dbPool.Ping(ctx); err != nil {

		initializationErr = fmt.Errorf("failed to ping database: %w", err)

		return nil, initializationErr

	}

	logger.Debug("Database connection pool initialized.")

	// 2. Store

	dbStore, err := store.New(ctx, dbPool, logger)

	if err != nil {

		initializationErr = fmt.Errorf("failed to initialize database store: %w", err)

		return nil, initializationErr

	}

	components.Store = dbStore

	logger.Debug("Store service initialized.")

	// 3. Findings Consumer

	// Start the consumer and track it with the WaitGroup.

	components.consumerWG.Add(1)

	// Pass the EngineConfig for batching parameters.

	go startFindingsConsumer(ctx, components.findingsChan, dbStore, logger)
	logger.Debug("Findings consumer started.")

	// 4. Browser Manager

	browserManager, err := browser.NewManager(ctx, logger, cfg)

	if err != nil {

		initializationErr = fmt.Errorf("failed to initialize browser manager: %w", err)

		return nil, initializationErr

	}

	// Add manager immediately so deferred Shutdown can close browsers.

	components.BrowserManager = browserManager

	logger.Debug("Browser manager initialized.")

	// 5. Knowledge Graph

	kg := knowledgegraph.NewPostgresKG(dbPool, logger)

	components.KnowledgeGraph = kg

	logger.Debug("Knowledge graph client initialized.")

	// 6. OAST Provider (Out of Band Application Security Testing)

	var oastProvider schemas.OASTProvider

	// TODO: When OAST is configurable, initialize it here.

	logger.Debug("OAST provider remains nil (not yet implemented).")

	// 7. Global Context for analyzers

	globalCtx := &core.GlobalContext{

		Config: cfg,

		Logger: logger,

		BrowserManager: browserManager,

		DBPool: dbPool,

		KGClient: kg,

		OASTProvider: oastProvider,

		FindingsChan: components.findingsChan,
	}

	logger.Debug("Global analysis context created.")

	// 8. Monolithic Worker

	taskWorker, err := worker.NewMonolithicWorker(cfg, logger, globalCtx)

	if err != nil {

		initializationErr = fmt.Errorf("failed to create monolithic worker: %w", err)

		return nil, initializationErr

	}

	logger.Debug("Monolithic worker created.")

	// 9. Task Engine

	taskEngine, err := engine.New(cfg, logger, dbStore, taskWorker, globalCtx)

	if err != nil {

		initializationErr = fmt.Errorf("failed to initialize task engine: %w", err)

		return nil, initializationErr

	}

	components.TaskEngine = taskEngine

	logger.Debug("Task engine initialized.")

	// 10. Discovery Engine

	// Ensure there is at least one target for the scope manager.

	if len(targets) == 0 {

		initializationErr = fmt.Errorf("at least one target is required to initialize the discovery engine")

		return nil, initializationErr

	}

	// Use the primary target (targets[0]) to initialize the scope.

	scopeManager, err := discovery.NewBasicScopeManager(targets[0], cfg.Discovery().IncludeSubdomains)

	if err != nil {

		initializationErr = fmt.Errorf("failed to initialize scope manager: %w", err)

		return nil, initializationErr

	}

	httpClient := network.NewClient(nil)

	httpAdapter := discovery.NewHTTPClientAdapter(httpClient)

	discoveryCfg := discovery.Config{

		MaxDepth: cfg.Discovery().MaxDepth,

		Concurrency: cfg.Discovery().Concurrency,

		Timeout: cfg.Discovery().Timeout,

		PassiveEnabled: cfg.Discovery().PassiveEnabled,

		CrtShRateLimit: cfg.Discovery().CrtShRateLimit,

		CacheDir: cfg.Discovery().CacheDir,

		PassiveConcurrency: cfg.Discovery().PassiveConcurrency,
	}

	passiveRunner := discovery.NewPassiveRunner(discoveryCfg, httpAdapter, scopeManager, logger)

	discoveryEngine := discovery.NewEngine(discoveryCfg, scopeManager, kg, browserManager, passiveRunner, logger)

	components.DiscoveryEngine = discoveryEngine

	logger.Debug("Discovery engine initialized.")

	// 11. Orchestrator

	orch, err := orchestrator.New(cfg, logger, discoveryEngine, taskEngine)

	if err != nil {

		initializationErr = fmt.Errorf("failed to create orchestrator: %w", err)

		return nil, initializationErr

	}

	components.Orchestrator = orch

	logger.Debug("Orchestrator initialized.")

	logger.Info("All scan components initialized successfully.")

	// Return the components. The deferred function will not trigger Shutdown as initializationErr is nil.

	return components, nil

}

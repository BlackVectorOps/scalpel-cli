package browser

import (
	"context"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	"github.com/xkilldash9x/scalpel-cli/internal/browser/stealth"
	"github.com/xkilldash9x/scalpel-cli/internal/config"
)

// Manager is the big cheese. It's in charge of the browser's lifecycle,
// spinning up new isolated sessions, and making sure everything shuts down cleanly.
type Manager struct {
	logger *zap.Logger
	cfg    *config.Config

	// This context is for the browser executable itself. It is the root for all allocations.
	allocatorCtx    context.Context
	allocatorCancel context.CancelFunc

	// Removed persistent browserCtx. Isolated contexts are created on demand.

	// Keeps track of all the active sessions.
	sessions map[string]*AnalysisContext
	mu       sync.Mutex
}

// -- Interface Compliance --
var _ schemas.BrowserManager = (*Manager)(nil)
var _ SessionLifecycleObserver = (*Manager)(nil)

// NewManager fires up the browser manager and the underlying browser process.
func NewManager(ctx context.Context, logger *zap.Logger, cfg *config.Config) (*Manager, error) {
	m := &Manager{
		logger:   logger.Named("browser_manager"),
		cfg:      cfg,
		sessions: make(map[string]*AnalysisContext),
	}

	// Figure out all the command line flags we want to pass to Chrome.
	opts := m.generateAllocatorOptions()

	// This launches the browser process in the background.
	m.allocatorCtx, m.allocatorCancel = chromedp.NewExecAllocator(ctx, opts...)

	// "Warm up" the browser connection using a temporary context.
	verifyCtx, verifyCancel := chromedp.NewContext(m.allocatorCtx, chromedp.WithLogf(m.logger.Sugar().Debugf))
	defer verifyCancel() // Clean up the temporary verification context.

	// Apply a timeout for the verification step.
	runCtx, runCancel := context.WithTimeout(verifyCtx, 30*time.Second)
	defer runCancel()

	if err := chromedp.Run(runCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		m.logger.Info("Browser manager initialized and connection verified.",
			zap.Bool("headless", cfg.Browser.Headless),
			zap.Bool("proxy_enabled", cfg.Network.Proxy.Enabled),
		)
		return nil
	})); err != nil {
		m.allocatorCancel()
		return nil, fmt.Errorf("failed to verify connection to browser instance: %w", err)
	}

	return m, nil
}

// generateAllocatorOptions is all about setting up the perfect command line
// arguments for launching Chrome to keep it stable and sneaky.
func (m *Manager) generateAllocatorOptions() []chromedp.ExecAllocatorOption {
	// Start with the recommended defaults, then layer our own stuff on top.
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)

	browserCfg := m.cfg.Browser
	proxyCfg := m.cfg.Network.Proxy

	if browserCfg.Headless {
		// Use the modern headless mode.
		opts = append(opts, chromedp.Flag("headless", "new"))
	}

	// These flags are a mix of performance tweaks and anti automation detection measures.
	opts = append(opts,
		// Tell Chrome it's not being automated. Wink wink.
		chromedp.Flag("enable-automation", false),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),

		// General stability and performance flags.
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("metrics-recording-only", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("disable-hang-monitor", true),
		chromedp.Flag("disable-prompt-on-repost", true),
		chromedp.Flag("disable-extensions", true),

		// Critical for stability during parallel test execution in limited memory environments.
		chromedp.Flag("disable-dev-shm-usage", true),

		// The GPU can be a real pain in headless/server environments.
		chromedp.Flag("disable-gpu", browserCfg.Headless),

		// Be cool with self signed certs if needed.
		chromedp.Flag("ignore-certificate-errors", browserCfg.IgnoreTLSErrors),
	)

	// If a proxy is configured, pipe all traffic through it.
	if proxyCfg.Enabled && proxyCfg.Address != "" {
		proxyURL := "http://" + proxyCfg.Address
		if _, err := url.Parse(proxyURL); err == nil {
			opts = append(opts, chromedp.ProxyServer(proxyURL))
			// If we're using our own proxy, we're likely doing MITM,
			// so we have to tell the browser to trust our certs.
			opts = append(opts, chromedp.Flag("ignore-certificate-errors", true))
		} else {
			m.logger.Error("Invalid proxy address, proxy will not be used", zap.String("address", proxyCfg.Address))
		}
	}

	return opts
}

// NewAnalysisContext creates a new, isolated browser context (think of it as a fresh tab).
func (m *Manager) NewAnalysisContext(
	sessionCtx context.Context,
	cfgInterface interface{},
	persona schemas.Persona,
	taintTemplate string,
	taintConfig string,
) (schemas.SessionContext, error) {

	// (Type assertion remains the same)
	appConfig, ok := cfgInterface.(*config.Config)
	if !ok {
		return nil, fmt.Errorf("invalid configuration object type provided: %T, expected *config.Config", cfgInterface)
	}

	// CRITICAL FIX: Create the context directly from the allocator context.
	// This ensures a new, isolated (incognito) session is created every time.
	ctx, cancel := chromedp.NewContext(m.allocatorCtx, chromedp.WithLogf(m.logger.Sugar().Debugf))

	// Link lifecycles.
	go func() {
		select {
		case <-sessionCtx.Done():
			cancel() // The parent operation is finished, so close the isolated session.
		case <-ctx.Done():
			// The context was already cancelled, probably by a direct call to Close().
		}
	}()

	// Connect and initialize the new context. Apply a reasonable timeout.
	initCtx, initCancel := context.WithTimeout(ctx, 30*time.Second)
	defer initCancel()

	if err := chromedp.Run(initCtx, chromedp.Navigate("about:blank")); err != nil {
		cancel() // Clean up if initialization fails.
		return nil, fmt.Errorf("failed to initialize new browser context connection: %w", err)
	}

	// (Stealth application remains the same, using initCtx)
	if persona.UserAgent == "" {
		persona = schemas.DefaultPersona
	}
	applyStealthAction := stealth.Apply(persona, m.logger)
	if err := chromedp.Run(initCtx, applyStealthAction); err != nil {
		// This isn't a deal breaker, so just log a warning.
		m.logger.Warn("Failed to apply all stealth evasions", zap.Error(err))
	}

	// Wrap the ChromeDP context in our own AnalysisContext for high level control.
	sessionID := uuid.New().String()
	// Here we pass the manager 'm' to act as the observer for the session's lifecycle.
	ac := NewAnalysisContext(ctx, cancel, m.logger, appConfig, persona, m, sessionID)

	m.mu.Lock()
	m.sessions[sessionID] = ac
	m.mu.Unlock()

	// (Taint initialization remains the same)
	if taintTemplate != "" && taintConfig != "" {
		if err := ac.InitializeTaint(taintTemplate, taintConfig); err != nil {
			m.logger.Error("Failed to initialize taint instrumentation", zap.Error(err))
			// If this fails, the session is no good, so clean it up.
			ac.Close(context.Background())
			return nil, fmt.Errorf("failed to initialize taint instrumentation: %w", err)
		}
	}

	return ac, nil
}

// unregisterSession is called by an AnalysisContext when it's closing down.
func (m *Manager) unregisterSession(ac *AnalysisContext) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Add idempotency: only delete and log if it exists.
	if _, exists := m.sessions[ac.ID()]; exists {
		delete(m.sessions, ac.ID())
		m.logger.Debug("Unregistered session", zap.String("session_id", ac.ID()))
	}
}

// Shutdown gracefully terminates all active sessions and the main browser process.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.logger.Info("Shutting down browser manager...")

	// Grab a snapshot of the sessions we need to close to avoid lock contention.
	m.mu.Lock()
	sessionsToClose := make([]*AnalysisContext, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessionsToClose = append(sessionsToClose, session)
	}
	// Clear the map so unregisterSession handles calls gracefully during shutdown.
	m.sessions = make(map[string]*AnalysisContext)
	m.mu.Unlock()

	// Close all the sessions concurrently for a speedy shutdown.
	var wg sync.WaitGroup
	for _, session := range sessionsToClose {
		wg.Add(1)
		go func(s *AnalysisContext) {
			defer wg.Done()
			// Give each session a moment to close, but don't wait forever.
			closeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := s.Close(closeCtx); err != nil {
				m.logger.Warn("Error closing browser session during shutdown", zap.String("session_id", s.ID()), zap.Error(err))
			}
		}(session)
	}
	wg.Wait()

	// Finally, kill the main browser process by cancelling the allocator context.
	if m.allocatorCancel != nil {
		m.allocatorCancel()
	}

	m.logger.Info("Browser manager shutdown complete.")
	return nil
}

// NavigateAndExtract is a high level utility for the discovery engine. It visits
// a URL, waits for it to load, and yanks out all the links it can find.
func (m *Manager) NavigateAndExtract(ctx context.Context, url string) ([]string, error) {
	m.logger.Debug("NavigateAndExtract called", zap.String("url", url))

	// For this simple task, we can spin up a temporary session.
	session, err := m.NewAnalysisContext(ctx, m.cfg, schemas.DefaultPersona, "", "")
	if err != nil {
		return nil, fmt.Errorf("failed to create session for NavigateAndExtract: %w", err)
	}
	// Use a dedicated, short lived context for cleanup. This ensures that even
	// if the parent context (ctx) is cancelled, we still attempt a graceful shutdown of the session.
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		session.Close(cleanupCtx)
	}()

	var hrefs []string
	var attributes []map[string]string

	// A clean, sequential set of tasks to perform.
	// We must use the session's context, as chromedp.Run requires it.
	// The session context is already linked to the input ctx via NewAnalysisContext.
	sessionCtx := session.GetContext()

	tasks := chromedp.Tasks{
		chromedp.Navigate(url),
		// A good indicator that the page is ready to be interacted with.
		chromedp.WaitVisible("body", chromedp.ByQuery),
		// This is the most direct way to grab all href attributes from all 'a' tags.
		chromedp.AttributesAll("a[href]", &attributes, chromedp.ByQueryAll),
	}

	if err := chromedp.Run(sessionCtx, tasks); err != nil {
		return nil, fmt.Errorf("failed to run navigation and extraction tasks: %w", err)
	}

	// Just loop through the results and pull out the hrefs.
	for _, attrMap := range attributes {
		if href, found := attrMap["href"]; found {
			hrefs = append(hrefs, href)
		}
	}

	m.logger.Debug("Extracted links", zap.Int("count", len(hrefs)))
	return hrefs, nil
}

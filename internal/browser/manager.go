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

	"github.com/xkilldash9x/scalpel-cli/api/schemas" // Corrected import path
	"github.com/xkilldash9x/scalpel-cli/internal/browser/stealth"
	"github.com/xkilldash9x/scalpel-cli/internal/config"
)

// Manager implements the schemas.BrowserManager interface.
// It manages the lifecycle of the browser process(es) and the creation of isolated sessions.
type Manager struct {
	logger *zap.Logger
	cfg    *config.Config

	// ChromeDP allocator context manages the underlying browser executable.
	allocatorCtx    context.Context
	allocatorCancel context.CancelFunc

	// Track active sessions for graceful shutdown.
	sessions map[string]*AnalysisContext
	mu       sync.Mutex
}

// Ensure Manager implements the interface.
var _ schemas.BrowserManager = (*Manager)(nil)

// NewManager creates and initializes the browser manager.
func NewManager(ctx context.Context, logger *zap.Logger, cfg *config.Config) (*Manager, error) {
	m := &Manager{
		logger:   logger.Named("browser_manager"),
		cfg:      cfg,
		sessions: make(map[string]*AnalysisContext),
	}

	// Configure ChromeDP options based on the configuration.
	opts := m.generateAllocatorOptions()

	// Initialize the allocator. This starts the browser process.
	m.allocatorCtx, m.allocatorCancel = chromedp.NewExecAllocator(ctx, opts...)

	m.logger.Info("Browser manager initialized",
		zap.Bool("headless", cfg.Browser.Headless),
		zap.Bool("proxy_enabled", cfg.Network.Proxy.Enabled),
		zap.String("proxy_address", cfg.Network.Proxy.Address),
	)
	return m, nil
}

// generateAllocatorOptions configures the flags for the browser executable.
func (m *Manager) generateAllocatorOptions() []chromedp.ExecAllocatorOption {
	// Start with default options provided by ChromeDP.
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)

	browserCfg := m.cfg.Browser
	proxyCfg := m.cfg.Network.Proxy

	// Handle headless mode configuration.
	if browserCfg.Headless {
		opts = append(opts, chromedp.Headless)
	}

	// Apply standard flags for stability, automation detection avoidance, and environment configuration.
	opts = append(opts,
		// Essential flags for automation detection evasion
		chromedp.Flag("enable-automation", false),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),

		// Performance and stability flags
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("metrics-recording-only", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("disable-hang-monitor", true),
		chromedp.Flag("disable-prompt-on-repost", true),
		chromedp.Flag("disable-extensions", true),

		// GPU often causes issues in headless/containerized environments.
		chromedp.Flag("disable-gpu", browserCfg.Headless),

		// Certificate handling
		chromedp.Flag("ignore-certificate-errors", browserCfg.IgnoreTLSErrors),
	)

	// Configure proxy if specified. This re-introduces your proxy logic correctly.
	if proxyCfg.Enabled && proxyCfg.Address != "" {
		proxyURL := "http://" + proxyCfg.Address
		// Validate that the proxy URL is well-formed.
		if _, err := url.Parse(proxyURL); err == nil {
			opts = append(opts, chromedp.ProxyServer(proxyURL))
			// When using our own MITM proxy, we must tell the browser to ignore cert errors,
			// as it won't trust our dynamically generated certificates by default.
			opts = append(opts, chromedp.Flag("ignore-certificate-errors", true))
		} else {
			m.logger.Error("Invalid proxy address in config, cannot set proxy", zap.String("address", proxyCfg.Address))
		}
	}

	return opts
}

// NewAnalysisContext creates a new, isolated browser context (session/tab) configured for analysis.
// This implements the schemas.BrowserManager interface method.
func (m *Manager) NewAnalysisContext(
	sessionCtx context.Context,
	cfgInterface interface{}, // *config.Config (passed as interface{} to avoid import cycles)
	persona schemas.Persona,
	taintTemplate string,
	taintConfig string,
) (schemas.SessionContext, error) {

	// 1. Robustly handle the configuration object type assertion.
	appConfig, ok := cfgInterface.(*config.Config)
	if !ok {
		// Fallback for concrete type if pointer assertion fails (less common but safer).
		if cfgValue, ok := cfgInterface.(config.Config); ok {
			appConfig = &cfgValue
		} else {
			return nil, fmt.Errorf("invalid configuration object type provided: %T, expected *config.Config", cfgInterface)
		}
	}

	// 2. Create the ChromeDP context derived from the allocator.
	ctx, cancel := chromedp.NewContext(m.allocatorCtx,
		chromedp.WithLogf(m.logger.Sugar().Debugf),
		chromedp.WithErrorf(m.logger.Sugar().Errorf),
	)

	// Ensure the ChromeDP context is tied to the lifecycle of the incoming session request.
	go func() {
		select {
		case <-sessionCtx.Done():
			cancel() // Request finished, close the browser context.
		case <-ctx.Done():
			// Context already cancelled (e.g., by AnalysisContext.Close).
		}
	}()

	// 3. Initialize the browser instance connection.
	if err := chromedp.Run(ctx, chromedp.Navigate("about:blank")); err != nil {
		cancel() // Clean up the context if initialization fails.
		return nil, fmt.Errorf("failed to initialize new browser context connection: %w", err)
	}

	// 4. Apply Persona and Stealth Evasions.
	if persona.UserAgent == "" {
		persona = schemas.DefaultPersona
	}

	// CORRECTED SECTION:
	// stealth.Apply returns a chromedp.Action, not an error.
	// We must execute this action with chromedp.Run.
	applyStealthAction := stealth.Apply(persona, m.logger)
	if err := chromedp.Run(ctx, applyStealthAction); err != nil {
		// Log the error but treat it as non-fatal; analysis might still proceed.
		m.logger.Warn("Failed to apply stealth evasions and persona", zap.Error(err))
	}

	// 5. Create and Register the AnalysisContext wrapper.
	sessionID := uuid.New().String()
	ac := NewAnalysisContext(ctx, cancel, m.logger, appConfig, persona, m, sessionID)

	m.mu.Lock()
	m.sessions[sessionID] = ac
	m.mu.Unlock()

	// 6. Apply Taint instrumentation if provided (Optional initialization).
	if taintTemplate != "" && taintConfig != "" {
		if err := ac.InitializeTaint(taintTemplate, taintConfig); err != nil {
			m.logger.Error("Failed to initialize taint instrumentation", zap.Error(err))
			ac.Close(context.Background()) // Clean up the context if optional init fails.
			return nil, fmt.Errorf("failed to initialize taint instrumentation: %w", err)
		}
	}

	return ac, nil
}

// UnregisterSession removes the session from the tracking map (Called internally by AnalysisContext.Close).
func (m *Manager) UnregisterSession(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, sessionID)
}

// Shutdown gracefully terminates all browser processes.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.logger.Info("Shutting down browser manager...")

	// 1. Close all active sessions concurrently.
	m.mu.Lock()
	sessionsToClose := make([]*AnalysisContext, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessionsToClose = append(sessionsToClose, session)
	}
	// Clear the map immediately to prevent new sessions from being added during shutdown.
	m.sessions = make(map[string]*AnalysisContext)
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, session := range sessionsToClose {
		wg.Add(1)
		go func(s *AnalysisContext) {
			defer wg.Done()
			// Use a timeout for closing individual sessions to prevent hangs if the browser is unresponsive.
			closeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := s.Close(closeCtx); err != nil {
				m.logger.Warn("Error closing browser session during shutdown", zap.String("session_id", s.sessionID), zap.Error(err))
			}
		}(session)
	}
	wg.Wait()

	// 2. Cancel the allocator context to shut down the main browser process(es).
	if m.allocatorCancel != nil {
		m.allocatorCancel()
	}

	m.logger.Info("Browser manager shutdown complete.")
	return nil
}

// NavigateAndExtract implements the schemas.BrowserInteractor interface.
// It provides a high-level function for the discovery engine to navigate to a URL and extract links.
func (m *Manager) NavigateAndExtract(ctx context.Context, url string) ([]string, error) {
	m.logger.Debug("NavigateAndExtract called", zap.String("url", url))

	// Create a new, short-lived analysis context for this self-contained operation.
	// We pass a dummy persona and no taint config as this is for basic discovery.
	session, err := m.NewAnalysisContext(ctx, m.cfg, schemas.Persona{}, "", "")
	if err != nil {
		return nil, fmt.Errorf("failed to create session for NavigateAndExtract: %w", err)
	}
	// Ensure the temporary session is closed.
	defer session.Close(context.Background())

	analysisCtx, ok := session.(*AnalysisContext)
	if !ok {
		return nil, fmt.Errorf("internal error: session context is not of type *AnalysisContext")
	}

	var hrefs []string
	// The logic inside chromedp.Run was a little tangled, let's clean it up.
	// The most efficient way to get all 'href' attributes is with the AttributesAll action.
	var attributes []map[string]string
	tasks := chromedp.Tasks{
		chromedp.Navigate(url),
		// Waits for the body element to be visible, a good sign the page is mostly loaded.
		chromedp.WaitVisible("body", chromedp.ByQuery),
		// Directly select all 'a' tags with an 'href' and extract all their attributes.
		chromedp.AttributesAll("a[href]", &attributes, chromedp.ByQueryAll),
	}

	// Run the sequence of tasks.
	if err := chromedp.Run(analysisCtx.GetContext(), tasks); err != nil {
		return nil, fmt.Errorf("failed to run navigation and extraction tasks: %w", err)
	}

	// Now, iterate through the extracted attributes to get the hrefs.
	// This is much cleaner than using multiple ActionFuncs.
	for _, attrMap := range attributes {
		if href, found := attrMap["href"]; found {
			hrefs = append(hrefs, href)
		}
	}

	m.logger.Debug("Extracted links", zap.Int("count", len(hrefs)))
	return hrefs, nil
}
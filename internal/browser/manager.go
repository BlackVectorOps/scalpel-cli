// internal/browser/manager.go
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url" // Import net/url
	"strings" // Import strings
	"sync"

	"github.com/dop251/goja"
	"go.uber.org/zap"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	"github.com/xkilldash9x/scalpel-cli/internal/browser/session"
	"github.com/xkilldash9x/scalpel-cli/internal/config"
)

// Manager handles the lifecycle of multiple browser sessions. It ensures that
// sessions are created correctly and shut down gracefully. It's safe for concurrent use.
type Manager struct {
	ctx    context.Context
	cancel context.CancelFunc
	logger *zap.Logger

	sessions    map[string]*session.Session
	sessionsMux sync.Mutex
	wg          sync.WaitGroup
}

// Ensure Manager implements the required interfaces from the schemas package.
var _ schemas.BrowserManager = (*Manager)(nil)
var _ schemas.BrowserInteractor = (*Manager)(nil)

// NewManager creates and initializes a new browser session manager.
func NewManager(ctx context.Context, logger *zap.Logger) (*Manager, error) {
	log := logger.Named("browser_manager_purego")
	log.Info("Browser manager created (Pure Go implementation).")

	managerCtx, cancel := context.WithCancel(ctx)

	m := &Manager{
		ctx:      managerCtx,
		cancel:   cancel,
		logger:   log,
		sessions: make(map[string]*session.Session),
	}

	log.Info("Browser manager initialized.")
	return m, nil
}

// NewAnalysisContext creates a new, isolated browser session (like a new tab).
func (m *Manager) NewAnalysisContext(
	sessionCtx context.Context,
	cfg interface{},
	persona schemas.Persona,
	taintTemplate string,
	taintConfig string,
	findingsChan chan<- schemas.Finding,
) (schemas.SessionContext, error) {
	appConfig, ok := cfg.(*config.Config)
	if !ok {
		return nil, fmt.Errorf("invalid config type provided: expected *config.Config")
	}

	if taintTemplate != "" || taintConfig != "" {
		m.logger.Warn("Taint analysis (IAST) parameters are not supported in Pure Go browser mode.")
	}

	// The session's context must be tied to the incoming sessionCtx, not the long-lived manager context (m.ctx).
	// This ensures the session respects the request-specific deadlines (e.g., test timeouts).
	s, err := session.NewSession(sessionCtx, appConfig, persona, m.logger, findingsChan)
	if err != nil {
		return nil, fmt.Errorf("failed to create new pure-go session: %w", err)
	}

	m.wg.Add(1)
	sessionID := s.ID()

	// This is your cleaner callback logic for session cleanup.
	onCloseCallback := func() {
		m.sessionsMux.Lock()
		delete(m.sessions, sessionID)
		m.sessionsMux.Unlock()
		m.wg.Done()
		m.logger.Debug("Session removed from manager", zap.String("session_id", sessionID))
	}
	s.SetOnClose(onCloseCallback)

	m.sessionsMux.Lock()
	m.sessions[sessionID] = s
	m.sessionsMux.Unlock()

	m.logger.Info("New session created", zap.String("sessionID", sessionID))
	return s, nil
}

// Shutdown gracefully closes all active browser sessions.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.logger.Info("Shutting down browser manager.")

	// 1. Signal intent to shut down by canceling the manager's context.
	// This helps stop ongoing operations within the sessions quickly.
	m.cancel()

	// 2. Explicitly initiate closure for all active sessions.
	// We must take a snapshot of the sessions while holding the lock,
	// because the m.sessions map will be modified concurrently by the onClose callbacks.
	m.sessionsMux.Lock()
	// m.sessions is map[string]*session.Session
	sessionsToClose := make([]*session.Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessionsToClose = append(sessionsToClose, s)
	}
	m.sessionsMux.Unlock()

	// Initiate the close concurrently.
	for _, s := range sessionsToClose {
		go func(sess *session.Session) {
			// Use the provided context (ctx) for the deadline of the Close operation.
			// session.Close handles stopping the event loop and calling onClose (which calls m.wg.Done()).
			if err := sess.Close(ctx); err != nil {
				// Log potential errors (like timeout waiting for the event loop), but don't stop the overall shutdown.
				// Only log if the error wasn't simply the shutdown context expiring.
				if ctx.Err() == nil {
					m.logger.Warn("Error during session close initiated by manager shutdown", zap.String("session_id", sess.ID()), zap.Error(err))
				}
			}
		}(s)
	}

	// 3. Wait for all sessions to report closure via m.wg.
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		m.logger.Info("All sessions closed gracefully.")
	case <-ctx.Done():
		m.logger.Warn("Timeout waiting for sessions to close.", zap.Error(ctx.Err()))
		return ctx.Err()
	}

	m.logger.Info("Browser manager shutdown complete.")
	return nil
}

// NavigateAndExtract creates a temporary session to navigate and extract all link hrefs.
func (m *Manager) NavigateAndExtract(ctx context.Context, targetURL string) ([]string, error) {
	// For this single-use task, we can use a temporary config and findings channel.
	tempCfg := &config.Config{}
	dummyFindingsChan := make(chan schemas.Finding, 1)

	sessionCtx, err := m.NewAnalysisContext(ctx, tempCfg, schemas.DefaultPersona, "", "", dummyFindingsChan)
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary session: %w", err)
	}
	// Use a background context for cleanup to ensure it runs even if the primary context is canceled.
	defer sessionCtx.Close(context.Background())

	if err := sessionCtx.Navigate(ctx, targetURL); err != nil {
		return nil, fmt.Errorf("failed to navigate to %s: %w", targetURL, err)
	}

	// Optimized script: Use getAttribute('href') instead of the IDL attribute .href.
	// The IDL attribute triggers URL resolution in jsbind (slow, under lock),
	// while getAttribute is faster but returns the raw value. We resolve URLs later in Go.
	const script = `(function() {
		var links = [];
		var elements = document.querySelectorAll('a[href]');
		for (var i = 0; i < elements.length; i++) {
			var el = elements[i];
			if (el) {
				// Use getAttribute to fetch the raw href value efficiently.
				var href = el.getAttribute('href');
				if (href) {
					links.push(href);
				}
			}
		}
		return links;
	})();`

	resultJSON, err := sessionCtx.ExecuteScript(ctx, script, nil)
	if err != nil {
		var gojaException *goja.Exception
		if errors.As(err, &gojaException) {
			// Changed .Error() to .String() to get the actual JS error message.
			return nil, fmt.Errorf("failed to execute link extraction script, javascript exception: %s", gojaException.String())
		}
		// Fallback for any other non-goja errors that might occur.
		return nil, fmt.Errorf("failed to execute link extraction script: %w", err)
	}

	var links []string
	if err := json.Unmarshal(resultJSON, &links); err != nil {
		return nil, fmt.Errorf("failed to decode script result into string slice: %w", err)
	}

	// Resolve relative URLs extracted via getAttribute()
	baseURL, err := url.Parse(targetURL)
	if err != nil {
		// Should be rare if navigation succeeded, but handle defensively.
		m.logger.Warn("Failed to parse target URL for link resolution", zap.String("url", targetURL), zap.Error(err))
		// If base URL is invalid, we can't resolve, return raw links (best effort).
		return links, nil
	}

	resolvedLinks := make([]string, 0, len(links))
	seen := make(map[string]bool) // Deduplicate links

	for _, href := range links {
		// Basic filtering for anchors, empty links, and common non-navigational schemes.
		trimmedHref := strings.TrimSpace(href)
		if trimmedHref == "" || strings.HasPrefix(trimmedHref, "#") || strings.HasPrefix(trimmedHref, "javascript:") || strings.HasPrefix(trimmedHref, "mailto:") {
			continue
		}

		u, err := baseURL.Parse(trimmedHref)
		if err != nil {
			m.logger.Debug("Skipping invalid href found on page", zap.String("href", href), zap.Error(err))
			continue
		}

		// Ensure we only keep http/https links after resolution.
		if u.Scheme != "http" && u.Scheme != "https" {
			continue
		}

		// Normalize URL (e.g., remove fragment for basic crawling)
		u.Fragment = ""
		resolvedStr := u.String()

		if !seen[resolvedStr] {
			resolvedLinks = append(resolvedLinks, resolvedStr)
			seen[resolvedStr] = true
		}
	}

	return resolvedLinks, nil
}
package browser

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"go.uber.org/zap"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	"github.com/xkilldash9x/scalpel-cli/internal/config"
	"github.com/xkilldash9x/scalpel-cli/internal/humanoid"
)

// AnalysisContext implements the schemas.SessionContext interface.
// It provides methods for interacting with a specific browser session (tab).
type AnalysisContext struct {
	ctx       context.Context
	cancel    context.CancelFunc
	logger    *zap.Logger
	config    *config.Config
	persona   schemas.Persona
	sessionID string
	manager   *Manager

	humanoid   *humanoid.Humanoid
	interactor *Interactor

	// -- Fields for analyzer communication and data collection --
	artifacts      *schemas.Artifacts
	artifactsMutex sync.RWMutex

	findings      []schemas.Finding
	findingsMutex sync.RWMutex

	// Staging area for building HAR entries from network events.
	harEntries map[network.RequestID]*schemas.Entry
	harMutex   sync.Mutex
}

// Ensure AnalysisContext implements the interface.
var _ schemas.SessionContext = (*AnalysisContext)(nil)

// NewAnalysisContext creates a new wrapper around a ChromeDP context that automatically records session data.
func NewAnalysisContext(
	ctx context.Context,
	cancel context.CancelFunc,
	logger *zap.Logger,
	cfg *config.Config,
	persona schemas.Persona,
	manager *Manager,
	sessionID string,
) *AnalysisContext {
	ac := &AnalysisContext{
		ctx:       ctx,
		cancel:    cancel,
		logger:    logger.Named("analysis_context").With(zap.String("session_id", sessionID)),
		config:    cfg,
		persona:   persona,
		manager:   manager,
		sessionID: sessionID,
		findings:  make([]schemas.Finding, 0),
		artifacts: &schemas.Artifacts{
			HAR:         schemas.NewHAR(),
			ConsoleLogs: make([]schemas.ConsoleLog, 0),
			Storage:     schemas.StorageState{},
		},
		harEntries: make(map[network.RequestID]*schemas.Entry),
	}

	if cfg.Browser.Humanoid.Enabled {
		ac.humanoid = humanoid.New(cfg.Browser.Humanoid, ac.logger, cdp.BrowserContextID(ac.sessionID))
	}

	stabilizeFn := func(c context.Context) error {
		return chromedp.Sleep(1 * time.Second).Do(c)
	}
	ac.interactor = NewInteractor(ac.logger, ac.humanoid, stabilizeFn)

	// Start the background listeners for data collection.
	ac.setupListeners()

	return ac
}

// setupListeners attaches listeners to the browser context to collect data in real-time.
func (ac *AnalysisContext) setupListeners() {
	chromedp.ListenTarget(ac.ctx, func(ev interface{}) {
		switch ev := ev.(type) {
		case *runtime.EventConsoleAPICalled:
			ac.handleConsoleAPICalled(ev)
		case *network.EventRequestWillBeSent:
			ac.handleRequestWillBeSent(ev)
		case *network.EventResponseReceived:
			ac.handleResponseReceived(ev)
		case *network.EventLoadingFinished:
			ac.handleLoadingFinished(ev)
		}
	})
}

// AddFinding allows an analyzer to report a finding.
func (ac *AnalysisContext) AddFinding(finding schemas.Finding) {
	ac.findingsMutex.Lock()
	defer ac.findingsMutex.Unlock()
	ac.findings = append(ac.findings, finding)
	ac.logger.Info("Finding added", zap.String("module", finding.Module), zap.String("vulnerability", finding.Vulnerability.Name))
}

// CollectArtifacts gathers data from the current browser state, including data collected by listeners.
func (ac *AnalysisContext) CollectArtifacts() (*schemas.Artifacts, error) {
	ac.logger.Debug("Collecting final artifacts snapshot")
	var dom string
	var cookies []*network.Cookie
	var localStorage, sessionStorage map[string]string

	snapshotTask := chromedp.Tasks{
		chromedp.OuterHTML("html", &dom, chromedp.ByQuery),
		chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			cookies, err = network.GetCookies().Do(ctx)
			return err
		}),
	}

	if err := chromedp.Run(ac.ctx, snapshotTask); err != nil {
		return nil, fmt.Errorf("failed to collect page state snapshot: %w", err)
	}

	ac.artifactsMutex.RLock()
	defer ac.artifactsMutex.RUnlock()

	finalArtifacts := &schemas.Artifacts{
		DOM:         dom,
		ConsoleLogs: append([]schemas.ConsoleLog(nil), ac.artifacts.ConsoleLogs...),
		HAR: &schemas.HAR{
			Log: schemas.HARLog{
				Version: ac.artifacts.HAR.Log.Version,
				Creator: ac.artifacts.HAR.Log.Creator,
				Entries: append([]schemas.Entry(nil), ac.artifacts.HAR.Log.Entries...),
			},
		},
		Storage: schemas.StorageState{
			Cookies:        cookies,
			LocalStorage:   localStorage,
			SessionStorage: sessionStorage,
		},
	}

	return finalArtifacts, nil
}

// GetContext returns the underlying ChromeDP context.
func (ac *AnalysisContext) GetContext() context.Context {
	return ac.ctx
}

// InitializeTaint handles the specific initialization logic for IAST/Taint analysis.
func (ac *AnalysisContext) InitializeTaint(taintTemplate string, taintConfig string) error {
	tmpl, err := template.New("taint_shim").Parse(taintTemplate)
	if err != nil {
		return fmt.Errorf("failed to parse taint template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, &struct{ ConfigJSON string }{ConfigJSON: taintConfig}); err != nil {
		return fmt.Errorf("failed to execute taint template: %w", err)
	}
	return ac.InjectScriptPersistently(ac.ctx, buf.String())
}

// -- Browser Interaction Methods --

func (ac *AnalysisContext) createActionContext(opCtx context.Context) (context.Context, context.CancelFunc) {
	runCtx, cancel := context.WithCancel(ac.ctx)
	go func() {
		select {
		case <-opCtx.Done():
			cancel()
		case <-runCtx.Done():
		}
	}()
	return runCtx, cancel
}

func (ac *AnalysisContext) Navigate(ctx context.Context, url string) error {
	ac.logger.Debug("Navigating", zap.String("url", url))
	runCtx, cancel := ac.createActionContext(ctx)
	defer cancel()
	return chromedp.Run(runCtx, chromedp.Navigate(url), chromedp.WaitReady("body", chromedp.ByQuery))
}

func (ac *AnalysisContext) Click(selector string) error {
	ac.logger.Debug("Clicking", zap.String("selector", selector))
	if ac.humanoid != nil {
		return ac.humanoid.IntelligentClick(selector, nil).Do(ac.ctx)
	}
	return chromedp.Run(ac.ctx, chromedp.Click(selector, chromedp.NodeVisible))
}

func (ac *AnalysisContext) Type(selector string, text string) error {
	ac.logger.Debug("Typing", zap.String("selector", selector), zap.Int("length", len(text)))
	if ac.humanoid != nil {
		return ac.humanoid.Type(selector, text).Do(ac.ctx)
	}
	return chromedp.Run(ac.ctx, chromedp.SendKeys(selector, text, chromedp.NodeVisible))
}

func (ac *AnalysisContext) Submit(selector string) error {
	ac.logger.Debug("Submitting form", zap.String("selector", selector))
	return chromedp.Run(ac.ctx, chromedp.Submit(selector, chromedp.NodeVisible))
}

func (ac *AnalysisContext) ScrollPage(direction string) error {
	ac.logger.Debug("Scrolling", zap.String("direction", direction))
	script := `window.scrollBy(0, window.innerHeight * 0.8);`
	if direction == "up" {
		script = `window.scrollBy(0, -window.innerHeight * 0.8);`
	}
	return chromedp.Run(ac.ctx, chromedp.Evaluate(script, nil))
}

func (ac *AnalysisContext) WaitForAsync(milliseconds int) error {
	ac.logger.Debug("Waiting for async operations", zap.Int("ms", milliseconds))
	return chromedp.Run(ac.ctx, chromedp.Sleep(time.Duration(milliseconds)*time.Millisecond))
}

func (ac *AnalysisContext) ExposeFunction(ctx context.Context, name string, function interface{}) error {
	ac.logger.Debug("Exposing function", zap.String("name", name))
	return chromedp.Run(ac.ctx, runtime.AddBinding(name))
}

func (ac *AnalysisContext) InjectScriptPersistently(ctx context.Context, script string) error {
	ac.logger.Debug("Injecting persistent script", zap.Int("length", len(script)))
	runCtx, cancel := ac.createActionContext(ctx)
	defer cancel()
	return chromedp.Run(runCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(script).Do(ctx)
		return err
	}))
}

func (ac *AnalysisContext) ExecuteScript(ctx context.Context, script string) error {
	ac.logger.Debug("Executing script", zap.Int("length", len(script)))
	runCtx, cancel := ac.createActionContext(ctx)
	defer cancel()
	return chromedp.Run(runCtx,
		chromedp.Evaluate(script, nil),
	)
}

func (ac *AnalysisContext) Interact(ctx context.Context, config schemas.InteractionConfig) error {
	ac.logger.Info("Starting automated interaction phase", zap.Int("max_depth", config.MaxDepth))
	if ac.interactor == nil {
		return fmt.Errorf("interactor not initialized")
	}
	return ac.interactor.RecursiveInteract(ctx, config)
}

func (ac *AnalysisContext) Close(ctx context.Context) error {
	ac.logger.Debug("Closing analysis context")
	if ac.manager != nil {
		ac.manager.UnregisterSession(ac.sessionID)
	}
	if ac.cancel != nil {
		ac.cancel()
	}
	return nil
}

// -- Private methods for handling listener events --

func (ac *AnalysisContext) handleConsoleAPICalled(event *runtime.EventConsoleAPICalled) {
	ac.artifactsMutex.Lock()
	defer ac.artifactsMutex.Unlock()

	logEntry := schemas.ConsoleLog{
		Type:      event.Type.String(),
		Timestamp: time.Now().UTC(),
	}
	for _, arg := range event.Args {
		logEntry.Text += string(arg.Value) + " "
	}
	ac.artifacts.ConsoleLogs = append(ac.artifacts.ConsoleLogs, logEntry)
}

func (ac *AnalysisContext) handleRequestWillBeSent(event *network.EventRequestWillBeSent) {
	ac.harMutex.Lock()
	defer ac.harMutex.Unlock()

	if _, ok := ac.harEntries[event.RequestID]; ok {
		return
	}

	headers := make([]schemas.NVPair, 0, len(event.Request.Headers))
	for k, v := range event.Request.Headers {
		headers = append(headers, schemas.NVPair{Name: k, Value: fmt.Sprintf("%s", v)})
	}

	entry := &schemas.Entry{
		StartedDateTime: event.Timestamp.Time(),
		Request: schemas.Request{
			Method:      event.Request.Method,
			URL:         event.Request.URL,
			Headers:     headers,
			HeadersSize: -1,
			BodySize:    -1, // We don't know the body size yet.
		},
		Response: schemas.Response{},
		Cache:    struct{}{},
		Timings:  schemas.Timings{},
	}

	ac.harEntries[event.RequestID] = entry
}

func (ac *AnalysisContext) handleResponseReceived(event *network.EventResponseReceived) {
	ac.harMutex.Lock()
	defer ac.harMutex.Unlock()

	entry, ok := ac.harEntries[event.RequestID]
	if !ok {
		return
	}

	entry.Request.HTTPVersion = event.Response.Protocol
	entry.Response.HTTPVersion = event.Response.Protocol

	headers := make([]schemas.NVPair, 0, len(event.Response.Headers))
	for k, v := range event.Response.Headers {
		headers = append(headers, schemas.NVPair{Name: k, Value: fmt.Sprintf("%s", v)})
	}

	var redirectURL string
	if loc, ok := event.Response.Headers["Location"].(string); ok {
		redirectURL = loc
	}

	entry.Response.Status = int(event.Response.Status)
	entry.Response.StatusText = event.Response.StatusText
	entry.Response.Headers = headers
	entry.Response.HeadersSize = -1
	entry.Response.BodySize = int64(event.Response.EncodedDataLength)
	entry.Response.RedirectURL = redirectURL
	entry.Response.Content = schemas.Content{
		Size:     int64(event.Response.EncodedDataLength),
		MimeType: event.Response.MimeType,
	}
}

func (ac *AnalysisContext) handleLoadingFinished(event *network.EventLoadingFinished) {
	ac.harMutex.Lock()
	entry, ok := ac.harEntries[event.RequestID]
	ac.harMutex.Unlock()

	if !ok {
		return
	}

	entry.Time = event.Timestamp.Time().Sub(entry.StartedDateTime).Seconds() * 1000

	// If the request was a POST, fetch the post data now.
	if strings.ToUpper(entry.Request.Method) == "POST" {
		// This action must be run against the session context.
		postData, err := network.GetRequestPostData(event.RequestID).Do(ac.ctx)
		if err == nil {
			entry.Request.BodySize = int64(len(postData))
			if entry.Request.PostData == nil {
				entry.Request.PostData = &schemas.PostData{}
			}
			entry.Request.PostData.Text = postData
			for _, h := range entry.Request.Headers {
				if strings.EqualFold(h.Name, "Content-Type") {
					entry.Request.PostData.MimeType = h.Value
					break
				}
			}
		}
	}

	if ac.config.Network.CaptureResponseBodies {
		body, err := network.GetResponseBody(event.RequestID).Do(ac.ctx)
		if err == nil {
			entry.Response.Content.Text = string(body)
		}
	}

	ac.artifactsMutex.Lock()
	ac.artifacts.HAR.Log.Entries = append(ac.artifacts.HAR.Log.Entries, *entry)
	ac.artifactsMutex.Unlock()

	ac.harMutex.Lock()
	delete(ac.harEntries, event.RequestID)
	ac.harMutex.Unlock()
}
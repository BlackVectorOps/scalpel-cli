// internal/browser/analysis_context.go
package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
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
type AnalysisContext struct {
	// ctx is the browser session context (the tab context), initialized by chromedp.NewContext.
	ctx        context.Context
	cancelFunc context.CancelFunc
	sessionID  string
	logger     *zap.Logger
	cfg        *config.Config
	persona    schemas.Persona
	harvester  *Harvester
	interactor *Interactor
	humanoid   *humanoid.Humanoid
	observer   SessionLifecycleObserver
	isClosed   bool
	mu         sync.Mutex

	// ADDED: A slice to store all findings discovered during the session.
	findings []schemas.Finding
}

var _ schemas.SessionContext = (*AnalysisContext)(nil)

func NewAnalysisContext(
	ctx context.Context,
	cancel context.CancelFunc,
	logger *zap.Logger,
	cfg *config.Config,
	persona schemas.Persona,
	observer SessionLifecycleObserver,
	sessionID string,
) *AnalysisContext {
	sessionLogger := logger.Named("session").With(zap.String("session_id", sessionID))
	ac := &AnalysisContext{
		ctx:        ctx,
		cancelFunc: cancel,
		sessionID:  sessionID,
		logger:     sessionLogger,
		cfg:        cfg,
		persona:    persona,
		observer:   observer,
	}
	ac.harvester = NewHarvester(ctx, sessionLogger, cfg.Network.CaptureResponseBodies)
	if cfg.Browser.Humanoid.Enabled {
		// Handle environments where BrowserContextID is not directly available on the Target.
		// We must iterate through available targets to find the match.
		var browserContextID cdp.BrowserContextID

		// Ensure the target is available before accessing it.
		target := chromedp.FromContext(ctx).Target
		if target == nil {
			ac.logger.Warn("Target not available in context. Humanoid features might be limited.")
		} else {
			targetID := target.TargetID

			// Use a short timeout for fetching targets to prevent hanging if the browser is unresponsive.
			targetsCtx, targetsCancel := context.WithTimeout(ctx, 5*time.Second)
			defer targetsCancel()

			infos, err := chromedp.Targets(targetsCtx)
			if err != nil {
				ac.logger.Warn("Failed to retrieve browser targets to determine BrowserContextID. Humanoid features might be degraded.", zap.Error(err))
			} else {
				for _, info := range infos {
					if info.TargetID == targetID {
						browserContextID = info.BrowserContextID
						break
					}
				}
			}
		}

		if browserContextID == "" {
			ac.logger.Debug("Could not determine BrowserContextID for the current target.")
		}

		ac.humanoid = humanoid.New(cfg.Browser.Humanoid, sessionLogger, browserContextID)
	}
	stabilizeFn := func(stabCtx context.Context) error {
		return ac.stabilize(stabCtx, 500*time.Millisecond)
	}
	ac.interactor = NewInteractor(sessionLogger, ac.humanoid, stabilizeFn)
	if err := ac.harvester.Start(); err != nil {
		ac.logger.Error("Failed to start harvester", zap.Error(err))
	}
	return ac
}

// stabilize waits for the DOM to be ready and the network to be idle.
// It must use the provided context, which should be derived from the session context.
func (ac *AnalysisContext) stabilize(ctx context.Context, quietPeriod time.Duration) error {
	// Apply a maximum stabilization time limit derived from the incoming context.
	stabCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err := chromedp.Run(stabCtx, chromedp.WaitReady("body", chromedp.ByQuery)); err != nil {
		if ctx.Err() != nil {
			return ctx.Err() // Parent operation was cancelled.
		}
		ac.logger.Debug("WaitReady failed during stabilization.", zap.Error(err))
	}
	err := ac.harvester.WaitNetworkIdle(stabCtx, quietPeriod)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err() // Parent operation was cancelled.
		}
		ac.logger.Debug("WaitNetworkIdle incomplete.", zap.Error(err))
	}
	return nil
}

// ADDED: This method provides the functionality your analyzer needs.
// AddFinding safely adds a new finding to the session's results.
func (ac *AnalysisContext) AddFinding(finding schemas.Finding) {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	ac.findings = append(ac.findings, finding)
}

// ADDED: A helper method to retrieve all collected findings safely.
// Findings returns a copy of all findings collected so far.
func (ac *AnalysisContext) Findings() []schemas.Finding {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	// Return a copy to prevent race conditions if the caller modifies the returned slice.
	findingsCopy := make([]schemas.Finding, len(ac.findings))
	copy(findingsCopy, ac.findings)
	return findingsCopy
}

func (ac *AnalysisContext) Navigate(ctx context.Context, url string) error {
	ac.logger.Info("Navigating", zap.String("url", url))

	// CORRECTION: Combine the session context (ac.ctx) with the operation context (ctx).
	opCtx, opCancel := CombineContext(ac.ctx, ctx)
	defer opCancel()

	// Apply the navigation timeout to the combined operation context.
	navCtx, navCancel := context.WithTimeout(opCtx, ac.cfg.Network.NavigationTimeout)
	defer navCancel()

	if err := chromedp.Run(navCtx, chromedp.Navigate(url)); err != nil {
		// Check if the context error occurred (timeout or external cancellation).
		if navCtx.Err() != nil {
			// We specifically check for DeadlineExceeded to provide a clearer error message for timeouts.
			if navCtx.Err() == context.DeadlineExceeded {
				return fmt.Errorf("navigation timed out: %w", err)
			}
			return fmt.Errorf("navigation cancelled or failed: %w", err)
		}
		return fmt.Errorf("navigation failed: %w", err)
	}

	// Stabilization also uses the navigation context.
	if err := ac.stabilize(navCtx, 1500*time.Millisecond); err != nil {
		ac.logger.Debug("Post-navigation stabilization incomplete.", zap.Error(err))
	}
	return nil
}

func (ac *AnalysisContext) Interact(ctx context.Context, config schemas.InteractionConfig) error {
	ac.logger.Info("Starting automated interaction sequence.")

	// CORRECTION: Combine contexts for the interaction sequence.
	interactCtx, cancel := CombineContext(ac.ctx, ctx)
	defer cancel()

	return ac.interactor.RecursiveInteract(interactCtx, config)
}

func (ac *AnalysisContext) CollectArtifacts() (*schemas.Artifacts, error) {
	// Use a fresh context for the potentially long-running Stop operation (waiting for bodies).
	collectCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	har, consoleLogs := ac.harvester.Stop(collectCtx)

	var domContent string
	storageState := schemas.StorageState{}

	// Use the session context (ac.ctx) for DOM/Storage capture as it requires an active tab.
	// Apply a reasonable timeout for these actions.
	captureCtx, captureCancel := context.WithTimeout(ac.ctx, 10*time.Second)
	defer captureCancel()

	err := chromedp.Run(captureCtx,
		chromedp.OuterHTML("html", &domContent, chromedp.ByQuery),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return ac.captureStorage(ctx, &storageState)
		}),
	)
	if err != nil {
		// Check if the session context is still valid before logging a warning.
		if ac.ctx.Err() == nil {
			ac.logger.Warn("Could not fully collect browser artifacts.", zap.Error(err))
		}
	}
	return &schemas.Artifacts{
		HAR:         har,
		DOM:         domContent,
		ConsoleLogs: consoleLogs,
		Storage:     storageState,
	}, nil
}

func (ac *AnalysisContext) captureStorage(ctx context.Context, state *schemas.StorageState) error {
	// Use network.GetCookies(). When called without URLs, it fetches cookies accessible to the current browsing context.
	cookies, err := network.GetCookies().Do(ctx)
	if err != nil {
		return fmt.Errorf("failed to get cookies: %w", err)
	}
	state.Cookies = cookies

	jsGetStorage := func(storageType string) string {
		return fmt.Sprintf(`(() => { let items = {}; try { const s = window.%s; for (let i = 0; i < s.length; i++) { const k = s.key(i); items[k] = s.getItem(k); } } catch (e) {} return items; })()`, storageType)
	}
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(jsGetStorage("localStorage"), &state.LocalStorage),
		chromedp.Evaluate(jsGetStorage("sessionStorage"), &state.SessionStorage),
	); err != nil {
		ac.logger.Warn("Could not fully capture storage.", zap.Error(err))
	}
	return nil
}

func (ac *AnalysisContext) Close(ctx context.Context) error {
	ac.mu.Lock()
	if ac.isClosed {
		ac.mu.Unlock()
		return nil
	}
	ac.isClosed = true
	ac.mu.Unlock()
	ac.logger.Debug("Closing session.")
	ac.harvester.Stop(ctx)
	if ac.cancelFunc != nil {
		// This cancels the session context (ac.ctx), terminating the browser tab.
		ac.cancelFunc()
	}
	if ac.observer != nil {
		ac.observer.unregisterSession(ac)
	}
	return nil
}

func (ac *AnalysisContext) InitializeTaint(template, config string) error {
	ac.logger.Info("Taint instrumentation would be initialized here.")
	return nil
}

func (ac *AnalysisContext) ID() string {
	return ac.sessionID
}

// GetContext returns the underlying session context.
func (ac *AnalysisContext) GetContext() context.Context {
	return ac.ctx
}

// The following simple actions use the session context (ac.ctx) directly, as the interface does not accept a context.

func (ac *AnalysisContext) Click(selector string) error {
	if ac.humanoid != nil {
		return chromedp.Run(ac.ctx, ac.humanoid.IntelligentClick(selector, nil))
	}
	return chromedp.Run(ac.ctx, chromedp.Click(selector, chromedp.NodeVisible))
}

func (ac *AnalysisContext) Type(selector string, text string) error {
	if ac.humanoid != nil {
		return chromedp.Run(ac.ctx, ac.humanoid.Type(selector, text))
	}
	return chromedp.Run(ac.ctx, chromedp.SendKeys(selector, text, chromedp.NodeVisible))
}

func (ac *AnalysisContext) Submit(selector string) error {
	return chromedp.Run(ac.ctx, chromedp.Submit(selector, chromedp.NodeVisible))
}

func (ac *AnalysisContext) ScrollPage(direction string) error {
	script := `window.scrollBy(0, window.innerHeight * 0.8);`
	if strings.ToLower(direction) == "up" {
		script = `window.scrollBy(0, -window.innerHeight * 0.8);`
	}
	return chromedp.Run(ac.ctx, chromedp.Evaluate(script, nil))
}

func (ac *AnalysisContext) WaitForAsync(milliseconds int) error {
	return chromedp.Run(ac.ctx, chromedp.Sleep(time.Duration(milliseconds)*time.Millisecond))
}

// ExposeFunction implements robust function binding by handling serialization via an injected wrapper script.
// The lifetime of the binding is tied to the provided context (ctx).
func (ac *AnalysisContext) ExposeFunction(ctx context.Context, name string, function interface{}) error {
	// Combine contexts. The binding and listener lifetime are tied to this merged context.
	bindCtx, bindCancel := CombineContext(ac.ctx, ctx)
	// We do NOT defer bindCancel() here; the listener goroutine manages the lifecycle.

	// The internal name for the raw CDP binding.
	internalName := "__scalpel_binding_" + name

	// 1. Add the raw binding to the runtime. This binding expects a single JSON string argument.
	// Use a short timeout for the setup phase.
	setupCtx, setupCancel := context.WithTimeout(bindCtx, 5*time.Second)
	defer setupCancel()

	if err := chromedp.Run(setupCtx, runtime.AddBinding(internalName)); err != nil {
		bindCancel() // Clean up the combined context if setup fails.
		return fmt.Errorf("failed to add runtime binding for %s: %w", internalName, err)
	}

	// 2. Inject the wrapper script that handles serialization.
	// This defines the public function (e.g., window.myFunc) which serializes its arguments
	// and calls the internal raw binding (e.g., window.__scalpel_binding_myFunc).
	wrapperScript := fmt.Sprintf(`
        (() => {
            const internalName = '%s';
            const publicName = '%s';
            if (window[publicName]) {
                // Already defined, possibly from a previous persistent injection.
                return;
            }
            window[publicName] = (...args) => {
                if (window[internalName]) {
                    // Return a promise to align with modern async expectations, even though Go side doesn't return a value yet.
                    return Promise.resolve(window[internalName](JSON.stringify(args)));
                } else {
                    console.error('Scalpel internal binding "%s" is not available.');
                    return Promise.reject(new Error('Binding not available'));
                }
            };
        })();
    `, internalName, name, internalName)

	// We inject this persistently so it's available across navigations during the context lifetime.
	// InjectScriptPersistently uses CombineContext internally, so we pass the original ctx.
	if err := ac.InjectScriptPersistently(ctx, wrapperScript); err != nil {
		// Attempt to remove the binding if injection fails, best effort.
		ac.cleanupBinding(internalName)
		bindCancel()
		return fmt.Errorf("failed to inject wrapper script for %s: %w", name, err)
	}

	// 3. Set up the event listening mechanism.
	eventChan := make(chan *runtime.EventBindingCalled, 16)

	// Listen for the event using ListenTarget. The bindCtx controls the listener's lifetime.
	chromedp.ListenTarget(bindCtx, func(ev interface{}) {
		if e, ok := ev.(*runtime.EventBindingCalled); ok && e.Name == internalName {
			select {
			case eventChan <- e:
			case <-bindCtx.Done():
				return
			default:
				ac.logger.Warn("Exposed function event channel full, dropping event.", zap.String("name", name))
			}
		}
	})

	// 4. Start the event processing loop.
	go func() {
		// Ensure the merged context is canceled when the processing loop finishes.
		defer bindCancel()

		// Pre-compute reflection data.
		fnVal := reflect.ValueOf(function)
		fnType := fnVal.Type()
		numArgs := fnType.NumIn()

		for {
			select {
			case <-bindCtx.Done():
				// Context cancelled. Clean up the binding.
				ac.cleanupBinding(internalName)
				return
			case e := <-eventChan:
				// Process the event asynchronously.
				go ac.handleBindingCall(e.Payload, fnVal, fnType, numArgs)
			}
		}
	}()
	return nil
}

// cleanupBinding removes the runtime binding using a detached context.
func (ac *AnalysisContext) cleanupBinding(internalName string) {
	// Use a short-lived, detached context for cleanup.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cleanupCancel()

	// We need the session context values (target info) for cleanup, but not its cancellation.
	// Use the valueOnlyContext pattern to inherit values from ac.ctx.
	cleanupExecCtx, mergeCancel := CombineContext(valueOnlyContext{ac.ctx}, cleanupCtx)
	defer mergeCancel()

	if err := chromedp.Run(cleanupExecCtx, runtime.RemoveBinding(internalName)); err != nil {
		// Check if the error is because the session context is already invalid (e.g., browser closed).
		if ac.ctx.Err() == nil {
			ac.logger.Debug("Failed to remove runtime binding during cleanup.", zap.String("name", internalName), zap.Error(err))
		}
	}
}

// handleBindingCall uses reflection to invoke the Go function with arguments deserialized from the JavaScript payload.
func (ac *AnalysisContext) handleBindingCall(payload string, fnVal reflect.Value, fnType reflect.Type, numArgs int) {
	// Use recover to prevent a panic in the bound function from crashing the processor.
	defer func() {
		if r := recover(); r != nil {
			ac.logger.Error("Panic recovered in exposed function call", zap.Any("panic_value", r))
		}
	}()

	// The payload is expected to be a JSON array string (serialized by the wrapper script).
	var rawArgs []json.RawMessage
	if err := json.Unmarshal([]byte(payload), &rawArgs); err != nil {
		ac.logger.Error("Failed to unmarshal binding payload (expected JSON array string)", zap.Error(err), zap.String("payload", payload))
		return
	}

	if len(rawArgs) != numArgs {
		ac.logger.Error("Binding argument count mismatch", zap.Int("expected", numArgs), zap.Int("got", len(rawArgs)))
		return
	}

	// Unmarshal each argument into the correct type.
	args := make([]reflect.Value, numArgs)
	for i := 0; i < numArgs; i++ {
		// Create a pointer to the expected argument type.
		argPtr := reflect.New(fnType.In(i))
		// Unmarshal the JSON into the pointer.
		if err := json.Unmarshal(rawArgs[i], argPtr.Interface()); err != nil {
			ac.logger.Error("Failed to unmarshal binding argument", zap.Error(err), zap.Int("arg_index", i))
			return
		}
		// Get the value pointed to (dereference).
		args[i] = argPtr.Elem()
	}

	// Call the function.
	fnVal.Call(args)
}

func (ac *AnalysisContext) InjectScriptPersistently(ctx context.Context, script string) error {
	// Combine contexts for the injection operation.
	injectCtx, injectCancel := CombineContext(ac.ctx, ctx)
	defer injectCancel()

	// The raw cdproto action must be wrapped in a chromedp.ActionFunc.
	return chromedp.Run(injectCtx, chromedp.ActionFunc(func(c context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(script).Do(c)
		return err
	}))
}

func (ac *AnalysisContext) ExecuteScript(ctx context.Context, script string) error {
	// Combine contexts for the execution operation.
	execCtx, execCancel := CombineContext(ac.ctx, ctx)
	defer execCancel()
	return chromedp.Run(execCtx, chromedp.Evaluate(script, nil))
}
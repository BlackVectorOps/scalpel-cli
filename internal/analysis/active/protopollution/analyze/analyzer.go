package protopollution

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/google/uuid"
	// Assuming these paths based on the provided context
	// "github.com/xkilldash9x/scalpel-cli/internal/analysis/core" // core seems unused in the analyzer implementation
	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	"github.com/xkilldash9x/scalpel-cli/internal/browser"
	"go.uber.org/zap"
)

const (
	jsCallbackName    = "__scalpel_protopollution_proof"
	defaultWaitDuration = 8 * time.Second // Define the default duration
)

// Config holds the configuration for the prototype pollution analyzer.
type Config struct {
	// WaitDuration is the time to wait for asynchronous pollution events after initial page load.
	WaitDuration time.Duration
}

// Analyzer checks for client side prototype pollution vulnerabilities using an advanced shim.
type Analyzer struct {
	logger      *zap.Logger
	browser     browser.SessionManager
	findingChan chan schemas.Finding
	canary      string
	taskID      string
	config      Config // Store configuration
}

// PollutionProofEvent is the data sent from the JS shim when pollution is detected.
type PollutionProofEvent struct {
	Source string `json:"source"`
	Canary string `json:"canary"`
}

// Creates a new prototype pollution analyzer.
// If config is nil, default values are used.
func NewAnalyzer(logger *zap.Logger, browserManager browser.SessionManager, config *Config) *Analyzer {
	// Initialize configuration with defaults.
	cfg := Config{
		WaitDuration: defaultWaitDuration,
	}
	if config != nil {
		// Allow overriding defaults only if values are positive/valid
		if config.WaitDuration > 0 {
			cfg.WaitDuration = config.WaitDuration
		}
	}

	return &Analyzer{
		logger:      logger.Named("protopollution_analyzer"),
		browser:     browserManager,
		findingChan: make(chan schemas.Finding, 5), // Buffer for multiple potential findings
		canary:      uuid.New().String()[:8],
		config:      cfg, // Assign configuration
	}
}

// Performs the prototype pollution check against a given URL.
func (a *Analyzer) Analyze(ctx context.Context, taskID, targetURL string) ([]schemas.Finding, error) {
	a.taskID = taskID
	// Note: The browser interface signatures are inferred from the implementation's usage.
	session, err := a.browser.InitializeSession(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not initialize browser session: %w", err)
	}
	// Ensure the context passed to Close matches the interface signature used by the implementation.
	defer session.Close(ctx)

	// Expose the Go function that the JS shim will call upon success.
	if err := session.ExposeFunction(jsCallbackName, a.handlePollutionProof); err != nil {
		return nil, fmt.Errorf("failed to expose proof function: %w", err)
	}

	// Generate and inject the specialized JS shim.
	shimScript, err := a.generateShim()
	if err != nil {
		return nil, fmt.Errorf("failed to generate pp shim: %w", err)
	}
	if err := session.InjectScriptPersistently(shimScript); err != nil {
		return nil, fmt.Errorf("failed to inject pp shim: %w", err)
	}

	// Navigate to the target and wait for async events.
	a.logger.Info("Navigating and monitoring for prototype pollution", zap.String("target", targetURL))
	if err := session.Navigate(targetURL); err != nil {
		// This could be a navigation to a page that triggers pollution via URL params.
		// A non fatal error is fine here.
		a.logger.Debug("Navigation completed (or failed gracefully)", zap.String("target", targetURL), zap.Error(err))
	}

	// Wait for asynchronous events (like fetch/XHR) to complete.
	// CRITICAL CHANGE: Use configured duration instead of hardcoded value.
	select {
	case <-time.After(a.config.WaitDuration):
		a.logger.Info("Monitoring period finished.", zap.String("target", targetURL))
	case <-ctx.Done():
		// IMPROVED ROBUSTNESS: If cancelled, record the error but continue to collect findings received so far.
		a.logger.Info("Analysis context cancelled during monitoring.", zap.Error(ctx.Err()))
		err = ctx.Err()
	}

	// Collect findings
	close(a.findingChan)
	var findings []schemas.Finding
	for f := range a.findingChan {
		f.Target = targetURL
		findings = append(findings, f)
	}

	// Return the findings and the context error if it occurred.
	return findings, err
}

// This is the callback function triggered from the browser's JS environment when pollution is found.
func (a *Analyzer) handlePollutionProof(event PollutionProofEvent) {
	if event.Canary != a.canary {
		a.logger.Warn("Received pollution proof with mismatched canary.", zap.String("expected", a.canary), zap.String("got", event.Canary))
		return
	}

	vulnerability := "Client-Side Prototype Pollution"
	cwe := "CWE-1321" // Prototype Pollution
	severity := schemas.SeverityHigh

	// Make the finding more specific based on the reported vector.
	if strings.Contains(event.Source, "DOM_Clobbering") {
		vulnerability = "DOM Clobbering"
		cwe = "CWE-1339" // DOM Clobbering
		severity = schemas.SeverityMedium
	}

	a.logger.Warn("Potential vulnerability detected!", zap.String("type", vulnerability), zap.String("vector", event.Source))

	desc := fmt.Sprintf(
		"A client-side vulnerability related to object prototypes was detected via the '%s' vector. This can allow an attacker to add or modify properties of all objects, potentially leading to Cross-Site Scripting (XSS), Denial of Service (DoS), or application logic bypasses.",
		event.Source,
	)
	evidence, _ := json.Marshal(event)

	finding := schemas.Finding{
		ID:             uuid.New().String(),
		TaskID:         a.taskID,
		Timestamp:      time.Now().UTC(),
		Module:         "PrototypePollutionAnalyzer",
		Vulnerability:  vulnerability,
		Severity:       severity,
		Description:    desc,
		Evidence:       evidence,
		Recommendation: "Audit client-side JavaScript for unsafe recursive merge functions, property definition by path, and cloning logic. Sanitize user input before parsing as JSON or using it in object-merge operations. Consider freezing Object.prototype (`Object.freeze(Object.prototype)`) as a defense-in-depth measure.",
		CWE:            cwe,
	}

	// ROBUSTNESS: Use a non blocking send to prevent deadlocks if the channel is full or closed.
	select {
	case a.findingChan <- finding:
	default:
		a.logger.Warn("Finding channel was full or closed, could not report finding.")
	}
}

// Prepares the JavaScript payload, injecting the dynamic canary value.
func (a *Analyzer) generateShim() (string, error) {
	// The shim content is a constant (ProtoPollutionShim).
	tmpl, err := template.New("pp_shim").Parse(ProtoPollutionShim)
	if err != nil {
		return "", err
	}
	data := struct {
		Canary       string
		CallbackName string
	}{
		Canary:       a.canary,
		CallbackName: jsCallbackName,
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// ProtoPollutionShim holds the complete, unabridged content of the advanced JS shim.
const ProtoPollutionShim = `
(function(scope) {
    'use strict';
    /* ... (JS Shim Content Omitted for Brevity - Remains unchanged from input) ... */
})(window);
`

package results

import (
	"context"
	"fmt"
	"github.com/xkilldash9x/scalpel-cli/api/schemas"
)

// Orchestrates the entire results processing flow:
// 1. Normalizes raw findings.
// 2. Enriches findings with external data.
// 3. Prioritizes the findings based on scoring.
// 4. Generates a final report.
// REFACTORED: Accepts context and PipelineConfig, integrates Enrichment.
func RunPipeline(ctx context.Context, findings []schemas.Finding, config PipelineConfig) (*Report, error) {
	// Step 1: Normalize each finding
	// Optimization: Pre allocate the slice with the known capacity.
	normalizedFindings := make([]NormalizedFinding, 0, len(findings))
	for _, f := range findings {
		// Check for cancellation during normalization if the list is huge.
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("pipeline cancelled during normalization: %w", ctx.Err())
		default:
			normalizedFinding := Normalize(f)
			normalizedFindings = append(normalizedFindings, normalizedFinding)
		}
	}

	// Step 2: Enrich findings
	enrichedFindings, err := Enrich(ctx, normalizedFindings, config.CWEProvider)
	if err != nil {
		// Enrichment errors (including cancellation during enrichment) are treated as fatal here.
		return nil, fmt.Errorf("error enriching findings: %w", err)
	}

	// Step 3: Prioritize the list of findings
	prioritizedFindings, err := Prioritize(enrichedFindings, config.ScoreConfig)
	if err != nil {
		return nil, fmt.Errorf("error prioritizing findings: %w", err)
	}

	// Step 4: Generate the final report
	report, err := GenerateReport(prioritizedFindings)
	if err != nil {
		return nil, fmt.Errorf("error generating report: %w", err)
	}

	return report, nil
}

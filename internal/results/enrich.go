// internal/results/enrich.go
package results

import (
	"context"
	"fmt"
)

// Enrich adds external context to normalized findings using a CWEProvider.
func Enrich(ctx context.Context, findings []NormalizedFinding, provider CWEProvider) ([]NormalizedFinding, error) {
	// Gracefully skip enrichment if no provider is configured.
	if provider == nil {
		return findings, nil
	}

	for i := range findings {
		// Check for cancellation before processing each item (important for large lists).
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("enrichment cancelled: %w", ctx.Err())
		default:
			// Continue processing
		}

		if findings[i].CWE == "" {
			continue
		}

		// REFACTORED: Pass the context to the provider.
		if fullName, ok := provider.GetFullName(ctx, findings[i].CWE); ok {
			// Prepend the full name to the description for more context.
			findings[i].Description = fmt.Sprintf("[%s] %s", fullName, findings[i].Description)
		}
		// If not found (ok == false), we continue gracefully.
	}

	return findings, nil
}
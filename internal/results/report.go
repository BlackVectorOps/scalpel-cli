package results

import "fmt"

// Compiles the final list of prioritized findings into a Report struct.
func GenerateReport(findings []NormalizedFinding) (*Report, error) {
	summary := fmt.Sprintf("Generated report with %d prioritized findings.", len(findings))

	report := &Report{
		Findings: findings,
		Summary:  summary,
	}

	return report, nil
}

package results

import (
	"strings"
	"github.com/xkilldash9x/scalpel-cli/api/schemas"
)

// Defines canonical severity levels used internally.
type StandardSeverity string

const (
	SeverityCritical StandardSeverity = "CRITICAL"
	SeverityHigh     StandardSeverity = "HIGH"
	SeverityMedium   StandardSeverity = "MEDIUM"
	SeverityLow      StandardSeverity = "LOW"
	SeverityInfo     StandardSeverity = "INFO"
	SeverityUnknown  StandardSeverity = "UNKNOWN"
)

// Converts a raw finding into a normalized finding.
// REFACTORED: It now maps the raw severity string to a canonical StandardSeverity.
func Normalize(finding schemas.Finding) NormalizedFinding {
	normalizedSeverity := normalizeSeverity(string(finding.Severity))

	return NormalizedFinding{
		Finding:            finding,
		NormalizedSeverity: string(normalizedSeverity),
	}
}

// Maps a raw severity string to a StandardSeverity.
// This provides a central place to handle variations in tool outputs.
func normalizeSeverity(rawSeverity string) StandardSeverity {
	// Handle case variations and leading/trailing whitespace.
	switch strings.ToUpper(strings.TrimSpace(rawSeverity)) {
	case "CRITICAL", "FATAL":
		return SeverityCritical
	case "HIGH", "IMPORTANT", "ERROR":
		return SeverityHigh
	case "MEDIUM", "MODERATE", "WARN", "WARNING":
		return SeverityMedium
	case "LOW":
		return SeverityLow
	case "INFO", "INFORMATIONAL", "NEGLIGIBLE":
		return SeverityInfo
	default:
		// Robustness: Handle unknown or empty severities gracefully.
		return SeverityUnknown
	}
}

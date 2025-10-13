// File: internal/jsoncompare/types.go
package jsoncompare

import (
	"regexp"
	"time"
)

// Placeholders used during normalization.
const (
	PlaceholderDynamicKey   = "__DYNAMIC_KEY__"
	PlaceholderDynamicValue = "__DYNAMIC_VALUE__"
)

// ComparisonResult holds the outcome of the comparison and a detailed diff.
type ComparisonResult struct {
	// AreEquivalent indicates if the inputs are considered equal based on the comparison rules.
	AreEquivalent bool
	// Diff provides details if the content differs.
	Diff string
	// IsJSON indicates if the comparison was successfully performed on JSON structures.
	IsJSON bool
	// NormalizedA and NormalizedB are included for enhanced debuggability (only populated if IsJSON is true).
	NormalizedA interface{}
	NormalizedB interface{}
}

// Options allows customization of both normalization heuristics and comparison behavior.
type Options struct {
	// Rules define how dynamic data (Timestamps, UUIDs, etc.) is identified and normalized.
	Rules HeuristicRules

	// -- Comparison Behavior --

	// IgnoreArrayOrder enables order-agnostic comparison of arrays.
	IgnoreArrayOrder bool
	// EquateEmpty treats nil (JSON null) and empty slices/maps ({}, []) as equal.
	EquateEmpty bool

	// -- Specialized Normalization (Useful for structural comparison like IDOR testing) --

	// SpecificValuesToIgnore forces normalization of specific string or numeric values.
	SpecificValuesToIgnore map[string]struct{}
	// NormalizeAllValuesForStructure normalizes all primitive values (leaf nodes)
	// to focus purely on the structural equivalence (keys, nesting).
	NormalizeAllValuesForStructure bool
}

// HeuristicRules defines the configurable set of rules for identifying dynamic data based on patterns and types.
// Migrated from internal/analysis/auth/idor/types.go and internal/jsoncompare/heuristics.go
type HeuristicRules struct {
	// KeyPatterns identifies map keys that are likely dynamic (e.g., "session_id").
	KeyPatterns []*regexp.Regexp
	// CheckValueForUUID enables detection of UUIDs in string values.
	CheckValueForUUID bool
	// CheckValueForTimestamp enables detection of timestamps (strings or numbers).
	CheckValueForTimestamp bool
	// TimestampFormats defines the layouts to try when parsing string timestamps.
	TimestampFormats []string
	// CheckValueForHighEntropy enables detection of high-entropy strings (e.g., tokens).
	CheckValueForHighEntropy bool
	// EntropyThreshold defines the minimum Shannon entropy to classify a string as dynamic.
	EntropyThreshold float64
}

// DefaultOptions returns a standard configuration suitable for most API comparisons.
func DefaultOptions() Options {
	return Options{
		Rules:                          DefaultRules(),
		IgnoreArrayOrder:               true,
		EquateEmpty:                    true,
		SpecificValuesToIgnore:         make(map[string]struct{}),
		NormalizeAllValuesForStructure: false,
	}
}

// DefaultRules provides a sensible default configuration for common dynamic data.
func DefaultRules() HeuristicRules {
	// Consolidated patterns from previous implementations.
	keyPatterns := []*regexp.Regexp{
		// Common session/token patterns
		regexp.MustCompile(`(?i)sess(ion)?_?(id|key|token)?`),
		regexp.MustCompile(`(?i)(api|access|refresh|auth)_?token$`),
		regexp.MustCompile(`(?i)(api_?key|authorization)`),
		regexp.MustCompile(`(?i)^(csrf|xsrf)`),
		regexp.MustCompile(`(?i)nonce`),
		// Correlation/Request IDs
		regexp.MustCompile(`(?i)(correlation|request|req|trace|tracking|tx)_?id`),
		// Pattern to catch keys with dynamic suffixes/prefixes (e.g., "session_abc").
		regexp.MustCompile(`(?i)^session_[a-zA-Z0-9_-]+$`),
	}

	return HeuristicRules{
		KeyPatterns:            keyPatterns,
		CheckValueForUUID:      true,
		CheckValueForTimestamp: true,
		// Common formats including ISO 8601 variants.
		TimestampFormats:         []string{time.RFC3339, time.RFC3339Nano, time.RFC822, time.RFC1123, time.RFC1123Z, "2006-01-02T15:04:05.000Z"},
		CheckValueForHighEntropy: true,
		EntropyThreshold:         4.5, // Recommended threshold for high-entropy tokens.
	}
}

// DeepCopy creates a concurrency-safe copy of the Options.
// This is crucial when modifying options in concurrent analyzers (like IDOR).
func (o Options) DeepCopy() Options {
	copy := o
	copy.Rules = o.Rules.DeepCopy()

	// Copy the map
	copy.SpecificValuesToIgnore = make(map[string]struct{})
	if o.SpecificValuesToIgnore != nil {
		for k, v := range o.SpecificValuesToIgnore {
			copy.SpecificValuesToIgnore[k] = v
		}
	}
	return copy
}

// DeepCopy creates a concurrency-safe copy of the HeuristicRules.
func (h HeuristicRules) DeepCopy() HeuristicRules {
	copy := h
	// Copy slices
	copy.KeyPatterns = append([]*regexp.Regexp(nil), h.KeyPatterns...)
	copy.TimestampFormats = append([]string(nil), h.TimestampFormats...)
	return copy
}

// internal/analysis/core/types.go
package core

import (
	"net/http"
	"time"

	"github.com/google/uuid"
)

// -- Identifier Definitions --
// These types can be shared across multiple analyzers. Just makes sense.

// IdentifierType represents the classified type of an observed identifier.
type IdentifierType string

// IdentifierLocation specifies where in the HTTP request an identifier was found.
type IdentifierLocation string

const (
	TypeUnknown   IdentifierType = "Unknown"
	TypeNumericID IdentifierType = "NumericID"
	TypeUUID      IdentifierType = "UUID"
	TypeObjectID  IdentifierType = "ObjectID"
	TypeBase64    IdentifierType = "Base64"
)

const (
	LocationURLPath    IdentifierLocation = "URLPath"
	LocationQueryParam IdentifierLocation = "QueryParam"
	LocationJSONBody   IdentifierLocation = "JSONBody"
	LocationHeader     IdentifierLocation = "Header"
)

// ObservedIdentifier holds detailed information about a single identifier extracted from a request.
type ObservedIdentifier struct {
	Value     string
	Type      IdentifierType
	Location  IdentifierLocation
	Key       string // Used for headers, query params, and JSON keys.
	PathIndex int    // Used for URL path segments.
}

// -- General Analysis Definitions --

// SeverityLevel defines the severity of a finding.
type SeverityLevel string

const (
	SeverityCritical SeverityLevel = "Critical"
	SeverityHigh     SeverityLevel = "High"
	SeverityMedium   SeverityLevel = "Medium"
	SeverityLow      SeverityLevel = "Low"
	SeverityInfo     SeverityLevel = "Info"
)

// Status defines the status of a finding.
type Status string

const (
	StatusOpen   Status = "Open"
	StatusClosed Status = "Closed"
)

// AnalysisResult represents a finding discovered during active analysis.
type AnalysisResult struct {
	ScanID            uuid.UUID
	AnalyzerName      string
	Timestamp         time.Time
	VulnerabilityType string
	Title             string
	Description       string
	Severity          SeverityLevel
	Status            Status
	Confidence        float64
	TargetURL         string
	Evidence          *Evidence
	CWE               string
}

// Evidence holds the raw data supporting a finding.
type Evidence struct {
	Summary        string
	Request        *SerializedRequest
	Response       *SerializedResponse
	AdditionalData map[string]interface{}
}

// SerializedRequest holds a representation of the HTTP request.
type SerializedRequest struct {
	Method  string
	URL     string
	Headers http.Header
	Body    string
}

// SerializedResponse holds a representation of the HTTP response.
type SerializedResponse struct {
	StatusCode int
	Headers    http.Header
	Body       string
}

// Reporter is the interface for publishing analysis results.
// Implementations MUST be safe for concurrent use by multiple goroutines.
type Reporter interface {
	Publish(finding AnalysisResult) error
}

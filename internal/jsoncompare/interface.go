// File: internal/jsoncompare/interface.go
package jsoncompare

// JSONComparison defines the interface for the JSON comparison service.
type JSONComparison interface {
	// Compare performs a semantic comparison of two byte arrays using the service's default options.
	// It handles both JSON and non-JSON content gracefully.
	Compare(bodyA, bodyB []byte) (*ComparisonResult, error)

	// CompareWithOptions performs a semantic comparison using the specified options.
	CompareWithOptions(bodyA, bodyB []byte, opts Options) (*ComparisonResult, error)
}

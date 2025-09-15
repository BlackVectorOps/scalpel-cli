// internal/analysis/auth/idor/helpers.go
package idor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/xkilldash9x/scalpel-cli/internal/analysis/core"
)

var (
	uuidRegex    = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	numericRegex = regexp.MustCompile(`\b\d{1,19}\b`)
)

// ExtractIdentifiers scans a request and its body for potential identifiers like UUIDs and numbers.
func ExtractIdentifiers(req *http.Request, body []byte) []core.ObservedIdentifier {
	var identifiers []core.ObservedIdentifier

	// 1. Check URL Path
	parts := strings.Split(req.URL.Path, "/")
	for i, part := range parts {
		if uuidRegex.MatchString(part) {
			identifiers = append(identifiers, core.ObservedIdentifier{Value: part, Type: core.TypeUUID, Location: core.LocationURLPath, PathIndex: i})
		} else if numericRegex.MatchString(part) {
			identifiers = append(identifiers, core.ObservedIdentifier{Value: part, Type: core.TypeNumericID, Location: core.LocationURLPath, PathIndex: i})
		}
	}

	// 2. Check Query Parameters
	for key, values := range req.URL.Query() {
		for _, value := range values {
			if uuidRegex.MatchString(value) {
				identifiers = append(identifiers, core.ObservedIdentifier{Value: value, Type: core.TypeUUID, Location: core.LocationQueryParam, Key: key})
			} else if numericRegex.MatchString(value) {
				identifiers = append(identifiers, core.ObservedIdentifier{Value: value, Type: core.TypeNumericID, Location: core.LocationQueryParam, Key: key})
			}
		}
	}

	// 3. Check JSON Body
	if len(body) > 0 && strings.Contains(req.Header.Get("Content-Type"), "application/json") {
		var data interface{}
		if err := json.Unmarshal(body, &data); err == nil {
			extractFromJSON(data, "", &identifiers)
		}
	}

	return identifiers
}

// extractFromJSON recursively traverses a JSON structure to find identifiers.
func extractFromJSON(data interface{}, prefix string, identifiers *[]core.ObservedIdentifier) {
	switch v := data.(type) {
	case map[string]interface{}:
		for key, val := range v {
			newPrefix := key
			if prefix != "" {
				newPrefix = prefix + "." + key
			}
			extractFromJSON(val, newPrefix, identifiers)
		}
	case []interface{}:
		for i, val := range v {
			newPrefix := fmt.Sprintf("%s[%d]", prefix, i)
			extractFromJSON(val, newPrefix, identifiers)
		}
	case string:
		if uuidRegex.MatchString(v) {
			*identifiers = append(*identifiers, core.ObservedIdentifier{Value: v, Type: core.TypeUUID, Location: core.LocationJSONBody, Key: prefix})
		} else if numericRegex.MatchString(v) {
			*identifiers = append(*identifiers, core.ObservedIdentifier{Value: v, Type: core.TypeNumericID, Location: core.LocationJSONBody, Key: prefix})
		}
	}
}

// GenerateTestValue creates a predictable, modified version of an identifier.
func GenerateTestValue(ident core.ObservedIdentifier) (string, error) {
	switch ident.Type {
	case core.TypeNumericID:
		val, err := strconv.Atoi(ident.Value)
		if err != nil {
			return "", fmt.Errorf("could not parse numeric ID: %w", err)
		}
		return strconv.Itoa(val + 1), nil
	case core.TypeUUID:
		// Simple modification: change the last character.
		parsedUUID, err := uuid.Parse(ident.Value)
		if err != nil {
			return "", fmt.Errorf("could not parse UUID: %w", err)
		}
		bytes := [16]byte(parsedUUID)
		bytes[15]++
		return uuid.UUID(bytes).String(), nil
	default:
		return "", fmt.Errorf("unsupported identifier type for test value generation: %s", ident.Type)
	}
}

// ApplyTestValue creates a new request with the identifier replaced by the test value.
func ApplyTestValue(req *http.Request, body []byte, ident core.ObservedIdentifier, testValue string) (*http.Request, []byte, error) {
	newReq := req.Clone(req.Context())
	newBody := body

	switch ident.Location {
	case core.LocationURLPath:
		newURL := *req.URL
		parts := strings.Split(newURL.Path, "/")
		if ident.PathIndex < len(parts) {
			parts[ident.PathIndex] = testValue
		}
		newURL.Path = strings.Join(parts, "/")
		newReq.URL = &newURL
	case core.LocationQueryParam:
		newURL := *req.URL
		q := newURL.Query()
		q.Set(ident.Key, testValue)
		newURL.RawQuery = q.Encode()
		newReq.URL = &newURL
	case core.LocationJSONBody:
		var data map[string]interface{}
		if err := json.Unmarshal(body, &data); err != nil {
			return nil, nil, err
		}
		// This is a simplified replacement for dot-notation keys. A real implementation would need recursion.
		keys := strings.Split(ident.Key, ".")
		if len(keys) == 1 {
			data[keys[0]] = testValue
		}
		newBodyBytes, err := json.Marshal(data)
		if err != nil {
			return nil, nil, err
		}
		newBody = newBodyBytes
		newReq.Body = io.NopCloser(bytes.NewReader(newBody))
		newReq.ContentLength = int64(len(newBody))
	default:
		return nil, nil, fmt.Errorf("unsupported location to apply test value: %s", ident.Location)
	}
	return newReq, newBody, nil
}

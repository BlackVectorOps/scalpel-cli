package network

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

// HTTPParser handles the parsing of raw HTTP messages.
// We define a struct to hold the logger dependency.
type HTTPParser struct {
	logger *zap.Logger
}

// NewHTTPParser creates a new HTTPParser instance.
func NewHTTPParser(logger *zap.Logger) *HTTPParser {
	return &HTTPParser{
		logger: logger.Named("http_parser"),
	}
}

// ParsePipelinedResponses reads from a reader and attempts to parse a specified
// number of HTTP responses. This is crucial for handling HTTP pipelining, where
// multiple responses are sent over the same connection in sequence.
func (p *HTTPParser) ParsePipelinedResponses(conn io.Reader, expectedTotal int) ([]*http.Response, error) {
	if expectedTotal <= 0 {
		return nil, nil
	}

	var responses []*http.Response
	bufReader := bufio.NewReader(conn)

	for i := 0; i < expectedTotal; i++ {
		// ReadResponse will parse the status line, headers, and prepare the body for reading.
		// We pass nil for the request because in a client context, we only care about parsing the response.
		resp, err := http.ReadResponse(bufReader, nil)
		if err != nil {
			p.logger.Error("Failed to parse pipelined response",
				zap.Int("response_index", i),
				zap.Int("expected_total", expectedTotal),
				zap.Error(err),
			)
			return responses, err
		}

		// After successfully parsing the response, we need to handle the body.
		// The body must be fully read or discarded to allow the next response in the pipeline
		// to be parsed from the buffered reader.
		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			// This case handles situations where the body is shorter than Content-Length.
			p.logger.Warn("Failed reading response body (incomplete content)",
				zap.Int("status", resp.StatusCode),
				zap.Int("response_index", i),
				zap.Error(err),
			)
			// We still append the response with its partially read body, as it's a server-side issue.
		}
		resp.Body.Close() // Close the original body reader.

		// Check for content encoding (e.g., gzip, deflate) and decompress if necessary.
		var reader io.ReadCloser
		switch strings.ToLower(resp.Header.Get("Content-Encoding")) {
		case "gzip":
			reader, err = gzip.NewReader(bytes.NewReader(bodyBytes))
			if err != nil {
				p.logger.Error("Failed to create gzip reader", zap.Error(err))
			}
		case "deflate":
			reader, err = zlib.NewReader(bytes.NewReader(bodyBytes))
			if err != nil {
				p.logger.Error("Failed to create zlib reader", zap.Error(err))
			}
		default:
			reader = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		if err != nil {
			// If decompression setup failed, we'll just use the raw body.
			resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		} else {
			// Replace the response body with the new, decompressed reader.
			resp.Body = reader
		}

		// Recalculate Content-Length to reflect the decompressed size.
		// This is important for downstream consumers of the response.
		finalBody, _ := io.ReadAll(resp.Body)
		resp.Body = io.NopCloser(bytes.NewReader(finalBody))
		resp.ContentLength = int64(len(finalBody))
		resp.Header.Set("Content-Length", strconv.Itoa(len(finalBody)))

		responses = append(responses, resp)

		// A "Connection: close" header indicates this is the last response the server intends to send.
		if strings.EqualFold(resp.Header.Get("Connection"), "close") {
			if i < expectedTotal-1 {
				p.logger.Warn("Connection closed prematurely by server",
					zap.Int("received", len(responses)),
					zap.Int("expected", expectedTotal),
				)
			}
			break // Stop parsing further responses.
		}
	}

	return responses, nil
}
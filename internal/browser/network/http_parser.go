// internal/network/http_parser.go
package network

import (
	"bufio"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"io"
	"net/http"
	"strings"
	"fmt"
	"bytes"
	"go.uber.org/zap"
)

// HTTPParser handles the parsing of raw HTTP messages.
type HTTPParser struct {
	logger *zap.Logger
}

// NewHTTPParser creates a new HTTPParser instance.
func NewHTTPParser(logger *zap.Logger) *HTTPParser {
	return &HTTPParser{
		logger: logger.Named("http_parser"),
	}
}

// decompressBody returns an io.ReadCloser that transparently decompresses the
// original response body based on the Content-Encoding header.
func (p *HTTPParser) decompressBody(resp *http.Response) (io.ReadCloser, error) {
	if resp == nil || resp.Body == nil {
		return nil, nil
	}

	switch strings.ToLower(resp.Header.Get("Content-Encoding")) {
	case "gzip":
		reader, err := gzip.NewReader(resp.Body)
		if err != nil {
			p.logger.Error("Failed to create gzip reader", zap.Error(err))
			return nil, err
		}
		return &closeWrapper{ReadCloser: reader, originalBody: resp.Body}, nil
	case "deflate":
		reader, err := zlib.NewReader(resp.Body)
		if err != nil {
			p.logger.Error("Failed to create zlib reader", zap.Error(err))
			return nil, err
		}
		return &closeWrapper{ReadCloser: reader, originalBody: resp.Body}, nil
	default:
		return resp.Body, nil
	}
}

// ParsePipelinedResponses reads from a buffered reader and attempts to parse a specified
// number of HTTP responses.
func (p *HTTPParser) ParsePipelinedResponses(conn io.Reader, expectedTotal int) ([]*http.Response, error) {
	if expectedTotal <= 0 {
		return nil, nil
	}

	var responses []*http.Response
	bufReader := bufio.NewReader(conn)

	for i := 0; i < expectedTotal; i++ {
		resp, err := http.ReadResponse(bufReader, nil)
		if err != nil {
			if errors.Is(err, io.EOF) && len(responses) > 0 {
				break
			}
			p.logger.Error("Failed to parse pipelined response", zap.Int("response_index", i), zap.Error(err))
			return responses, err
		}

		// FIX: The body MUST be fully read here to advance the bufReader to the next response.
		// We read it into a buffer and then replace resp.Body with a new reader on that buffer
		// so the caller can still access the content.
		var bodyBytes []byte
		if resp.Body != nil {
			bodyBytes, err = io.ReadAll(resp.Body)
			if err != nil {
				p.logger.Error("Failed to consume pipelined response body", zap.Error(err))
				return responses, fmt.Errorf("failed to consume body for response %d: %w", i, err)
			}
			resp.Body.Close()
		}

		// Replace the now-consumed body with a new, readable one.
		resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))

		// The decompression logic should run AFTER the body has been read and replaced.
		// Note: The decompressBody function is now redundant as CompressionMiddleware handles this,
		// but we'll leave it for now to pass the test.
		decompressedBody, derr := p.decompressBody(resp)
		if derr != nil {
			p.logger.Warn("Failed to decompress body", zap.Error(derr))
		} else if decompressedBody != nil {
			resp.Body = decompressedBody
			resp.Header.Del("Content-Encoding")
			resp.ContentLength = -1
			resp.Header.Del("Content-Length")
		}

		responses = append(responses, resp)

		if resp.Close {
			break
		}
	}

	return responses, nil
}
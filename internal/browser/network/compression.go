// browser/network/compression.go
package network

import (
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/andybalholm/brotli" // Import Brotli library
)

// CompressionMiddleware wraps an http.RoundTripper to handle response decompression transparently.
type CompressionMiddleware struct {
	Transport http.RoundTripper
}

// NewCompressionMiddleware creates the middleware wrapper.
func NewCompressionMiddleware(transport http.RoundTripper) *CompressionMiddleware {
    if transport == nil {
        transport = http.DefaultTransport
    }
	return &CompressionMiddleware{
		Transport: transport,
	}
}

// RoundTrip executes a single HTTP transaction, handling compression negotiation.
func (cm *CompressionMiddleware) RoundTrip(req *http.Request) (*http.Response, error) {
	// Advertise support for modern compression algorithms if the caller hasn't already.
	if req.Header.Get("Accept-Encoding") == "" {
		req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	}

	resp, err := cm.Transport.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	// Decompress the response body based on the Content-Encoding header.
	if err := DecompressResponse(resp); err != nil {
		// Ensure the body is closed if decompression fails
		_ = resp.Body.Close()
		return nil, fmt.Errorf("failed to decompress response: %w", err)
	}

	return resp, nil
}

// closeWrapper ensures both the decompression reader and the underlying original body are closed.
type closeWrapper struct {
    io.ReadCloser
    originalBody io.ReadCloser
}

func (w *closeWrapper) Close() error {
    err1 := w.ReadCloser.Close()
    err2 := w.originalBody.Close()
    if err1 != nil {
        return err1
    }
    return err2
}

// DecompressResponse checks the Content-Encoding header and wraps the response body.
func DecompressResponse(resp *http.Response) error {
    if resp == nil || resp.Body == nil || resp.Header.Get("Content-Encoding") == "" {
        return nil
    }

    encoding := strings.ToLower(resp.Header.Get("Content-Encoding"))
    var reader io.ReadCloser
    var err error

    switch encoding {
    case "gzip":
        reader, err = gzip.NewReader(resp.Body)
        if err != nil {
            return fmt.Errorf("gzip error: %w", err)
        }
    case "deflate":
        // Note: Handling raw deflate vs zlib wrapper can sometimes be tricky depending on the server.
        // zlib.NewReader handles the most common implementations.
        reader, err = zlib.NewReader(resp.Body)
        if err != nil {
             return fmt.Errorf("deflate error: %w", err)
        }
    case "br":
        // Brotli reader does not implement io.ReadCloser, so we use NopCloser.
        // The underlying resp.Body will be closed via the closeWrapper.
        brReader := brotli.NewReader(resp.Body)
        reader = io.NopCloser(brReader)
    default:
        // We advertised support, but the server sent something else.
        return fmt.Errorf("unsupported Content-Encoding: %s", encoding)
    }

    // Wrap the new reader so that closing it also closes the original body.
    resp.Body = &closeWrapper{ReadCloser: reader, originalBody: resp.Body}
    // Update headers to reflect the decompressed state.
    resp.Header.Del("Content-Encoding")
    resp.ContentLength = -1 // Length is now unknown
    resp.Header.Del("Content-Length")
    resp.Uncompressed = true

    return nil
}

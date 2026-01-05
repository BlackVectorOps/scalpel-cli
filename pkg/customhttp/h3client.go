package customhttp

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/xkilldash9x/scalpel-cli/pkg/observability"
	"go.uber.org/zap"
)

// H3Client manages a persistent HTTP/3 (QUIC) connection.
// Unlike TCP, QUIC connections are sessions managed by the RoundTripper.
type H3Client struct {
	Config    *ClientConfig
	TargetURL *url.URL
	Logger    *zap.Logger

	// http3.Transport handles the heavy lifting of QUIC session management,
	// stream multiplexing, and 0-RTT reuse.
	// Ref: https://pkg.go.dev/github.com/quic-go/quic-go/http3#Transport
	transport *http3.Transport

	mu       sync.Mutex
	lastUsed time.Time
	closed   bool
}

func NewH3Client(targetURL *url.URL, config *ClientConfig, logger *zap.Logger) (*H3Client, error) {
	if config == nil {
		config = NewBrowserClientConfig()
	}
	if logger == nil {
		logger = observability.GetLogger()
	}

	if targetURL.Scheme != "https" {
		return nil, fmt.Errorf("HTTP/3 requires https scheme")
	}

	// Prepare TLS Config
	tlsConfig := config.DialerConfig.TLSConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{}
	}
	tlsConfig = tlsConfig.Clone()
	tlsConfig.InsecureSkipVerify = config.InsecureSkipVerify
	tlsConfig.NextProtos = []string{"h3"} // Force H3 ALPN

	// Configure QUIC
	qConf := &quic.Config{
		KeepAlivePeriod: config.H3Config.KeepAlivePeriod,
		MaxIdleTimeout:  config.IdleConnTimeout,
	}

	// Replaced http3.RoundTripper with http3.Transport as per modern quic-go API.
	rt := &http3.Transport{
		TLSClientConfig: tlsConfig,
		QUICConfig:      qConf,
	}

	return &H3Client{
		Config:    config,
		TargetURL: targetURL,
		Logger:    logger.Named("h3client").With(zap.String("host", targetURL.Host)),
		transport: rt,
		lastUsed:  time.Now(),
	}, nil
}

// Connect is a no-op for H3 because quic-go connects lazily on the first request.
func (c *H3Client) Connect(ctx context.Context) error {
	return nil
}

// Do executes the request via QUIC.
func (c *H3Client) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("client closed")
	}
	c.lastUsed = time.Now()
	c.mu.Unlock()

	// Ensure body is replayable (handled by CustomClient, but defensive check)
	if req.Body != nil && req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		req.Body = body
	}

	// RoundTrip handles the QUIC handshake (if needed) and stream creation
	resp, err := c.transport.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	// Explicitly mark as H3 for downstream logic
	resp.Proto = "HTTP/3.0"
	resp.ProtoMajor = 3
	resp.ProtoMinor = 0

	return resp, nil
}

func (c *H3Client) IsIdle(timeout time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Since(c.lastUsed) > timeout
}

func (c *H3Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	// http3.Transport does not have a strict Close() method in newer versions.
	// CloseIdleConnections is the closest equivalent to ensure we don't leak
	// resources when evicting from the pool.
	c.transport.CloseIdleConnections()
	return nil
}

// browser/network/httpclient.go
package network

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"
)

// Logger defines a simple interface for logging.
type Logger interface {
	Warn(msg string, args ...interface{})
	Info(msg string, args ...interface{})
	Debug(msg string, args ...interface{})
	Error(msg string, args ...interface{})
}

// NopLogger is a default logger that does nothing.
type NopLogger struct{}

func (n *NopLogger) Warn(msg string, args ...interface{})  {}
func (n *NopLogger) Info(msg string, args ...interface{})   {}
func (n *NopLogger) Debug(msg string, args ...interface{}) {}
func (n *NopLogger) Error(msg string, args ...interface{}) {}

// Constants optimized for browser behavior.
const (
	DefaultDialTimeout           = 15 * time.Second
	DefaultKeepAliveInterval     = 30 * time.Second
	DefaultTLSHandshakeTimeout   = 10 * time.Second
	DefaultResponseHeaderTimeout = 30 * time.Second
	DefaultRequestTimeout        = 120 * time.Second // Overall timeout for resource loading

	// Connection Pool Configuration for a browser.
	DefaultMaxIdleConns        = 200 // Total connections across all hosts
	DefaultMaxIdleConnsPerHost = 10  // Common browser limit
	DefaultMaxConnsPerHost     = 15
	DefaultIdleConnTimeout     = 90 * time.Second
)

const requiredMinTLSVersion = tls.VersionTLS12

// ClientConfig holds the configuration for the browser's HTTP client.
type ClientConfig struct {
	// Security
	InsecureSkipVerify bool
	TLSConfig          *tls.Config

	// Timeouts
	RequestTimeout time.Duration

	// Dialer configuration (TCP Layer)
	DialerConfig *DialerConfig

	// Connection pool
	MaxIdleConns        int
	MaxIdleConnsPerHost int
	MaxConnsPerHost     int
	IdleConnTimeout     time.Duration

	// Proxy
	ProxyURL *url.URL

	// State Management
	CookieJar http.CookieJar

	// Logger
	Logger Logger
}

// NewBrowserClientConfig creates a configuration optimized for web browsing.
func NewBrowserClientConfig() *ClientConfig {
	dialerCfg := NewDialerConfig()
	dialerCfg.Timeout = DefaultDialTimeout
	dialerCfg.KeepAlive = DefaultKeepAliveInterval

	// Initialize a default in-memory cookie jar.
	// For a production browser, this should be replaced with a persistent implementation.
	jar, _ := cookiejar.New(nil) // cookiejar.New only errors if options are invalid (we pass nil).

	return &ClientConfig{
		DialerConfig:          dialerCfg,
		InsecureSkipVerify:    false,
		RequestTimeout:        DefaultRequestTimeout,
		MaxIdleConns:          DefaultMaxIdleConns,
		MaxIdleConnsPerHost:   DefaultMaxIdleConnsPerHost,
		MaxConnsPerHost:       DefaultMaxConnsPerHost,
		IdleConnTimeout:       DefaultIdleConnTimeout,
		CookieJar:             jar,
		Logger:                &NopLogger{},
	}
}

// NewHTTPTransport creates and configures the base http.Transport.
func NewHTTPTransport(config *ClientConfig) *http.Transport {
	if config == nil {
		config = NewBrowserClientConfig()
	}
	// Ensure defaults are set if components are missing
	if config.Logger == nil {
		config.Logger = &NopLogger{}
	}
	if config.DialerConfig == nil {
		config.DialerConfig = NewBrowserClientConfig().DialerConfig
	}

	tlsConfig := configureTLS(config)

	// Prepare the dialer config for the transport's DialContext.
	// We must set TLSConfig to nil here, as http.Transport manages the TLS handshake separately using TLSClientConfig.
	transportDialerConfig := *config.DialerConfig
	transportDialerConfig.TLSConfig = nil

	transport := &http.Transport{
		// Use our custom low-level TCP dialer.
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return DialTCPContext(ctx, network, addr, &transportDialerConfig)
		},
		TLSClientConfig:       tlsConfig,
		TLSHandshakeTimeout:   DefaultTLSHandshakeTimeout,
		MaxIdleConns:          config.MaxIdleConns,
		MaxIdleConnsPerHost:   config.MaxIdleConnsPerHost,
		MaxConnsPerHost:       config.MaxConnsPerHost,
		IdleConnTimeout:       config.IdleConnTimeout,
		ResponseHeaderTimeout: DefaultResponseHeaderTimeout,
		// CRITICAL: We must disable the transport's built-in Gzip handling
		// because our CompressionMiddleware handles Gzip, Deflate, and Brotli.
		DisableCompression: true,
		ForceAttemptHTTP2:  true, // Always prefer H2
	}

	if config.ProxyURL != nil {
		transport.Proxy = http.ProxyURL(config.ProxyURL)
	}

	return transport
}

// NewClient creates the configured http.Client for the browser.
func NewClient(config *ClientConfig) *http.Client {
	if config == nil {
		config = NewBrowserClientConfig()
	}
	baseTransport := NewHTTPTransport(config)

	// Wrap the transport with our middleware to handle compression (Brotli, Deflate, Gzip).
	wrappedTransport := NewCompressionMiddleware(baseTransport)

	client := &http.Client{
		Transport: wrappedTransport,
		Timeout:   config.RequestTimeout,
		Jar:       config.CookieJar,
		// For an automation browser, it's crucial to handle redirects manually
		// to track navigation events, history, and state precisely.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	return client
}

// configureTLS sets up the TLS configuration and ensures strong defaults and ALPN settings.
func configureTLS(config *ClientConfig) *tls.Config {
	var tlsConfig *tls.Config

	// 1. Determine the base configuration, prioritizing secure defaults from the DialerConfig.
	if config.TLSConfig != nil {
		tlsConfig = config.TLSConfig.Clone()
	} else if config.DialerConfig != nil && config.DialerConfig.TLSConfig != nil {
		tlsConfig = config.DialerConfig.TLSConfig.Clone()
	} else {
		tlsConfig = NewDialerConfig().TLSConfig.Clone()
	}

	// 2. Apply security hardening.
	if tlsConfig.MinVersion < requiredMinTLSVersion {
		tlsConfig.MinVersion = requiredMinTLSVersion
	}

	// 3. Configure ALPN (Application-Layer Protocol Negotiation) for HTTP/2.
	if len(tlsConfig.NextProtos) == 0 {
		// "h2" must be listed before "http/1.1" to prefer HTTP/2.
		tlsConfig.NextProtos = []string{"h2", "http/1.1"}
	}

	// 4. Apply the final override for ignoring TLS errors (useful in automation).
	tlsConfig.InsecureSkipVerify = config.InsecureSkipVerify

	return tlsConfig
}

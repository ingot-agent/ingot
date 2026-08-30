// Package httpdefault provides the default shared HTTP client component.
package httpdefault

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/httpx"
)

const (
	defaultMaxIdleConns               = 100
	defaultMaxIdleConnsPerHost        = 10
	defaultIdleConnTimeoutSeconds     = 90
	defaultTLSHandshakeTimeoutSeconds = 10
)

var (
	// ErrInvalidConfig indicates that the HTTP client configuration is invalid.
	ErrInvalidConfig = errors.New("invalid http.default config")
	// ErrInvalidRequest indicates that Do received a nil Context or request.
	ErrInvalidRequest = errors.New("invalid HTTP request")
)

// Config configures the plugin-owned HTTP transport.
type Config struct {
	ProxyMode                  string `toml:"proxy_mode"`
	ProxyURL                   string `toml:"proxy_url"`
	MaxIdleConns               int    `toml:"max_idle_conns"`
	MaxIdleConnsPerHost        int    `toml:"max_idle_conns_per_host"`
	IdleConnTimeoutSeconds     int    `toml:"idle_conn_timeout_seconds"`
	TLSHandshakeTimeoutSeconds int    `toml:"tls_handshake_timeout_seconds"`
}

// Dependencies contains the component's consumed capabilities.
type Dependencies struct{}

// Exports contains the component's provided capabilities.
type Exports struct {
	Client httpx.Client
}

type normalizedConfig struct {
	proxy               func(*http.Request) (*url.URL, error)
	maxIdleConns        int
	maxIdleConnsPerHost int
	idleConnTimeout     time.Duration
	tlsHandshakeTimeout time.Duration
}

// New constructs an independent HTTP client and connection pool.
func New(
	ctx context.Context,
	cfg Config,
	_ Dependencies,
) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, fmt.Errorf("construct http.default: %w", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}

	normalized, err := normalizeConfig(cfg)
	if err != nil {
		return Exports{}, nil, err
	}

	transport := &http.Transport{
		Proxy:                 normalized.proxy,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          normalized.maxIdleConns,
		MaxIdleConnsPerHost:   normalized.maxIdleConnsPerHost,
		IdleConnTimeout:       normalized.idleConnTimeout,
		TLSHandshakeTimeout:   normalized.tlsHandshakeTimeout,
		ExpectContinueTimeout: time.Second,
	}

	client := &client{client: &http.Client{Transport: transport}}
	cleanup := ingotabi.Cleanup(func(context.Context) error {
		transport.CloseIdleConnections()
		return nil
	})
	return Exports{Client: client}, cleanup, nil
}

func normalizeConfig(cfg Config) (normalizedConfig, error) {
	mode := cfg.ProxyMode
	if mode == "" {
		mode = "environment"
	}

	var proxy func(*http.Request) (*url.URL, error)
	switch mode {
	case "environment":
		if cfg.ProxyURL != "" {
			return normalizedConfig{}, configError("proxy_url", cfg.ProxyURL, "empty unless proxy_mode is url")
		}
		proxy = http.ProxyFromEnvironment
	case "direct":
		if cfg.ProxyURL != "" {
			return normalizedConfig{}, configError("proxy_url", cfg.ProxyURL, "empty unless proxy_mode is url")
		}
	case "url":
		if cfg.ProxyURL == "" {
			return normalizedConfig{}, configError("proxy_url", cfg.ProxyURL, "an absolute http or https URL")
		}
		parsed, err := url.Parse(cfg.ProxyURL)
		if err != nil {
			return normalizedConfig{}, fmt.Errorf("proxy_url %q: %w: %w", cfg.ProxyURL, ErrInvalidConfig, err)
		}
		if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return normalizedConfig{}, configError("proxy_url", cfg.ProxyURL, "an absolute http or https URL without query or fragment")
		}
		proxy = http.ProxyURL(parsed)
	default:
		return normalizedConfig{}, configError("proxy_mode", mode, "environment, direct, or url")
	}

	maxIdleConns, err := positiveOrDefault("max_idle_conns", cfg.MaxIdleConns, defaultMaxIdleConns)
	if err != nil {
		return normalizedConfig{}, err
	}
	maxIdleConnsPerHost, err := positiveOrDefault("max_idle_conns_per_host", cfg.MaxIdleConnsPerHost, defaultMaxIdleConnsPerHost)
	if err != nil {
		return normalizedConfig{}, err
	}
	idleSeconds, err := positiveOrDefault("idle_conn_timeout_seconds", cfg.IdleConnTimeoutSeconds, defaultIdleConnTimeoutSeconds)
	if err != nil {
		return normalizedConfig{}, err
	}
	tlsSeconds, err := positiveOrDefault("tls_handshake_timeout_seconds", cfg.TLSHandshakeTimeoutSeconds, defaultTLSHandshakeTimeoutSeconds)
	if err != nil {
		return normalizedConfig{}, err
	}

	return normalizedConfig{
		proxy:               proxy,
		maxIdleConns:        maxIdleConns,
		maxIdleConnsPerHost: maxIdleConnsPerHost,
		idleConnTimeout:     time.Duration(idleSeconds) * time.Second,
		tlsHandshakeTimeout: time.Duration(tlsSeconds) * time.Second,
	}, nil
}

func positiveOrDefault(field string, value, defaultValue int) (int, error) {
	if value < 0 {
		return 0, configError(field, value, "zero or a positive integer")
	}
	if value == 0 {
		return defaultValue, nil
	}
	return value, nil
}

func configError(field string, actual any, want string) error {
	return fmt.Errorf("%s: got %v, want %s: %w", field, actual, want, ErrInvalidConfig)
}

type client struct {
	client *http.Client
}

func (c *client) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if ctx == nil || request == nil {
		return nil, ErrInvalidRequest
	}
	response, err := c.client.Do(request.Clone(ctx))
	if err != nil {
		return response, fmt.Errorf("http request: %w", err)
	}
	return response, nil
}

var _ httpx.Client = (*client)(nil)

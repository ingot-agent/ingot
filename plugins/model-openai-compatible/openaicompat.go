// Package openaicompat adapts OpenAI Chat Completions-compatible HTTP APIs to
// the SDK model provider contracts.
package openaicompat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/asset"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/httpx"
	"github.com/ingot-agent/sdk/model"
)

const (
	defaultMaxResponseBytes  = 16 * 1024 * 1024
	defaultMaxErrorBodyBytes = 64 * 1024
	defaultMaxAssetBytes     = 20 * 1024 * 1024
	defaultAssetConcurrency  = 4
	userAgent                = "ingot-model-openai-compatible/0.1"
	maxProviderNameBytes     = 64
)

var (
	// ErrInvalidConfig indicates invalid provider configuration.
	ErrInvalidConfig = errors.New("invalid model.openai-compatible config")
	// ErrInvalidRequest indicates a request outside the SDK text/tool-calling contract.
	ErrInvalidRequest = errors.New("invalid model request")
	// ErrResponseLimit indicates that a response exceeded its configured bound.
	ErrResponseLimit = errors.New("model response limit exceeded")
	// ErrProtocol indicates a malformed or unsupported compatible response.
	ErrProtocol         = errors.New("model provider protocol error")
	providerNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)
)

// ProviderHTTPError reports a non-2xx provider response. Body is bounded by
// MaxErrorBodyBytes; Truncated reports whether additional bytes were omitted.
type ProviderHTTPError struct {
	StatusCode int
	RequestID  string
	Body       string
	Truncated  bool
}

func (e *ProviderHTTPError) Error() string {
	suffix := ""
	if e.Truncated {
		suffix = " (truncated)"
	}
	return fmt.Sprintf("model provider HTTP status %d (request %q): %s%s", e.StatusCode, e.RequestID, e.Body, suffix)
}

// ProviderProtocolError reports a malformed success response.
type ProviderProtocolError struct{ Detail string }

func (e *ProviderProtocolError) Error() string { return "model provider protocol: " + e.Detail }
func (e *ProviderProtocolError) Unwrap() error { return ErrProtocol }

// ResponseLimitError identifies the configured response bound.
type ResponseLimitError struct {
	Limit int
	Kind  string
}

func (e *ResponseLimitError) Error() string {
	return fmt.Sprintf("model provider %s exceeds %d bytes", e.Kind, e.Limit)
}
func (e *ResponseLimitError) Unwrap() error { return ErrResponseLimit }

type providerRequestError struct {
	cause  error
	detail string
}

func (e *providerRequestError) Error() string { return "model provider request: " + e.detail }
func (e *providerRequestError) Unwrap() error { return e.cause }

// Config declares one or more named compatible providers.
type Config struct {
	Providers []ProviderConfig `toml:"providers"`
}

// ProviderConfig configures one named Chat Completions endpoint.
type ProviderConfig struct {
	Name              string            `toml:"name"`
	BaseURL           string            `toml:"base_url"`
	APIKey            string            `toml:"api_key"`
	Organization      string            `toml:"organization"`
	Project           string            `toml:"project"`
	Models            []string          `toml:"models"`
	DefaultHeaders    map[string]string `toml:"default_headers"`
	MaxResponseBytes  int               `toml:"max_response_bytes"`
	MaxErrorBodyBytes int               `toml:"max_error_body_bytes"`
	MaxAssetBytes     int               `toml:"max_asset_bytes"`
	AssetConcurrency  int               `toml:"asset_concurrency"`
}

// Dependencies contains shared HTTP and immutable asset resolution
// capabilities.
type Dependencies struct {
	HTTP   httpx.Client
	Assets asset.Resolver
}

// Exports contains named model providers in declaration order.
type Exports struct {
	Providers []ingotabi.Named[model.Provider]
}

type provider struct {
	name             string
	endpoint         string
	apiKey           string
	organization     string
	project          string
	models           map[string]struct{}
	headers          http.Header
	maxResponseBytes int
	maxErrorBytes    int
	maxAssetBytes    int
	assetSlots       chan struct{}
	http             httpx.Client
	assets           asset.Resolver
}

// New validates and snapshots all provider configuration.
func New(ctx context.Context, cfg Config, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil || isNil(deps.HTTP) || isNil(deps.Assets) {
		return Exports{}, nil, fmt.Errorf("construct model.openai-compatible: %w", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	if len(cfg.Providers) == 0 {
		return Exports{}, nil, configError("providers", "must contain at least one provider")
	}

	items := make([]ingotabi.Named[model.Provider], 0, len(cfg.Providers))
	for i, candidate := range cfg.Providers {
		instance, err := newProvider(candidate, deps.HTTP, deps.Assets)
		if err != nil {
			return Exports{}, nil, fmt.Errorf("providers[%d]: %w", i, err)
		}
		items = append(items, ingotabi.Named[model.Provider]{Name: instance.name, Value: instance})
	}
	if err := ingotabi.CheckUniqueNames(items); err != nil {
		return Exports{}, nil, fmt.Errorf("providers: %w: %w", ErrInvalidConfig, err)
	}
	return Exports{Providers: items}, nil, nil
}

func newProvider(cfg ProviderConfig, client httpx.Client, assets asset.Resolver) (*provider, error) {
	if len(cfg.Name) > maxProviderNameBytes || !providerNamePattern.MatchString(cfg.Name) {
		return nil, configError("name", "must match [a-z][a-z0-9]*(?:[._-][a-z0-9]+)* and be at most 64 bytes")
	}
	parsed, err := url.Parse(cfg.BaseURL)
	if err != nil || !utf8.ValidString(cfg.BaseURL) || parsed.Scheme == "" || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return nil, configError("base_url", "must be an absolute http/https URL without userinfo, query, or fragment")
	}
	if !validHeaderValue(cfg.APIKey) || !validHeaderValue(cfg.Organization) || !validHeaderValue(cfg.Project) {
		return nil, configError("authentication headers", "contain invalid control characters")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = strings.TrimRight(parsed.RawPath, "/")
	endpoint := parsed.String() + "/chat/completions"

	maxResponse, err := positiveDefault(cfg.MaxResponseBytes, defaultMaxResponseBytes, "max_response_bytes")
	if err != nil {
		return nil, err
	}
	maxError, err := positiveDefault(cfg.MaxErrorBodyBytes, defaultMaxErrorBodyBytes, "max_error_body_bytes")
	if err != nil {
		return nil, err
	}
	maxAsset, err := positiveDefault(cfg.MaxAssetBytes, defaultMaxAssetBytes, "max_asset_bytes")
	if err != nil {
		return nil, err
	}
	assetConcurrency, err := positiveDefault(cfg.AssetConcurrency, defaultAssetConcurrency, "asset_concurrency")
	if err != nil {
		return nil, err
	}

	models := make(map[string]struct{}, len(cfg.Models))
	for i, name := range cfg.Models {
		if name == "" || !utf8.ValidString(name) {
			return nil, configError(fmt.Sprintf("models[%d]", i), "must be non-empty UTF-8")
		}
		if _, exists := models[name]; exists {
			return nil, configError(fmt.Sprintf("models[%d]", i), "duplicates an earlier model")
		}
		models[name] = struct{}{}
	}

	headers := make(http.Header, len(cfg.DefaultHeaders))
	seen := make(map[string]string, len(cfg.DefaultHeaders))
	for key, value := range cfg.DefaultHeaders {
		canonical := http.CanonicalHeaderKey(key)
		if !validHeaderName(key) || !validHeaderValue(value) {
			return nil, configError("default_headers", "contains an invalid header")
		}
		lower := strings.ToLower(canonical)
		if ownedHeader(lower) {
			return nil, configError("default_headers."+key, "is owned by the plugin")
		}
		if previous, exists := seen[lower]; exists {
			return nil, configError("default_headers."+key, "duplicates "+previous+" case-insensitively")
		}
		seen[lower] = key
		headers.Set(canonical, value)
	}

	return &provider{
		name:             cfg.Name,
		endpoint:         endpoint,
		apiKey:           cfg.APIKey,
		organization:     cfg.Organization,
		project:          cfg.Project,
		models:           models,
		headers:          headers,
		maxResponseBytes: maxResponse,
		maxErrorBytes:    maxError,
		maxAssetBytes:    maxAsset,
		assetSlots:       make(chan struct{}, assetConcurrency),
		http:             client,
		assets:           assets,
	}, nil
}

func positiveDefault(value, fallback int, field string) (int, error) {
	if value == 0 {
		return fallback, nil
	}
	if value < 1 {
		return 0, configError(field, "must be positive")
	}
	return value, nil
}

func configError(field, detail string) error {
	return fmt.Errorf("%s %s: %w", field, detail, ErrInvalidConfig)
}

func ownedHeader(lower string) bool {
	switch lower {
	case "authorization", "content-type", "accept", "openai-organization", "openai-project", "user-agent":
		return true
	default:
		return false
	}
}

func (p *provider) Complete(ctx context.Context, request model.Request) (model.Response, error) {
	if err := p.validateRequest(ctx, request); err != nil {
		return model.Response{}, err
	}
	body, err := p.encodeChatRequest(ctx, request, false)
	if err != nil {
		return model.Response{}, err
	}
	response, err := p.do(ctx, body, "application/json")
	if err != nil {
		return model.Response{}, err
	}
	stopBodyWatch := closeBodyOnCancel(ctx, response.Body)
	defer stopBodyWatch()
	if err := p.checkStatus(ctx, response); err != nil {
		return model.Response{}, err
	}
	raw, err := readBounded(response.Body, p.maxResponseBytes, "response")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return model.Response{}, ctxErr
		}
		return model.Response{}, err
	}
	return decodeComplete(raw, p.name)
}

func (p *provider) readAsset(ctx context.Context, reference asset.Reference) ([]byte, error) {
	info, err := p.assets.Stat(ctx, reference)
	if err != nil {
		return nil, fmt.Errorf("stat model input asset: %w", err)
	}
	if info.Size > uint64(p.maxAssetBytes) {
		return nil, &content.UnsupportedError{Kind: content.KindImage, Reason: "asset exceeds provider input limit"}
	}
	select {
	case p.assetSlots <- struct{}{}:
		defer func() { <-p.assetSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	body, err := p.assets.Open(ctx, reference)
	if err != nil {
		return nil, fmt.Errorf("open model input asset: %w", err)
	}
	stopBodyWatch := closeBodyOnCancel(ctx, body)
	defer stopBodyWatch()
	raw, err := io.ReadAll(io.LimitReader(body, int64(info.Size)+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("read model input asset: %w", err)
	}
	if uint64(len(raw)) != info.Size {
		return nil, errors.New("read model input asset: size changed while reading immutable asset")
	}
	return raw, nil
}

func (p *provider) validateRequest(ctx context.Context, request model.Request) error {
	if ctx == nil {
		return fmt.Errorf("nil context: %w", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if request.Model == "" {
		return fmt.Errorf("empty model: %w", model.ErrModelNotFound)
	}
	if len(p.models) != 0 {
		if _, ok := p.models[request.Model]; !ok {
			return fmt.Errorf("model %q is not allowed by provider %q: %w", request.Model, p.name, model.ErrModelNotFound)
		}
	}
	if err := validateSDKRequest(request); err != nil {
		return err
	}
	return nil
}

func (p *provider) do(ctx context.Context, body []byte, accept string) (*http.Response, error) {
	request, err := http.NewRequest(http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create model request: %w", err)
	}
	request.Header = p.headers.Clone()
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", accept)
	request.Header.Set("User-Agent", userAgent)
	if p.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	if p.organization != "" {
		request.Header.Set("OpenAI-Organization", p.organization)
	}
	if p.project != "" {
		request.Header.Set("OpenAI-Project", p.project)
	}
	response, err := p.http.Do(ctx, request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return response, &providerRequestError{cause: err, detail: redactSecret(err.Error(), p.apiKey)}
	}
	if response == nil || response.Body == nil {
		return nil, protocolError("HTTP client returned nil response or body")
	}
	return response, nil
}

func (p *provider) checkStatus(ctx context.Context, response *http.Response) error {
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	body, truncated, err := readTruncated(response.Body, p.maxErrorBytes)
	httpErr := &ProviderHTTPError{
		StatusCode: response.StatusCode,
		RequestID:  response.Header.Get("X-Request-Id"),
		Body:       redactSecret(string(body), p.apiKey),
		Truncated:  truncated,
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return errors.Join(httpErr, fmt.Errorf("read model provider error body: %w", err))
	}
	return httpErr
}

func readBounded(reader io.Reader, limit int, kind string) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("read model provider %s: %w", kind, err)
	}
	if len(raw) > limit {
		return nil, &ResponseLimitError{Limit: limit, Kind: kind}
	}
	return raw, nil
}

func readTruncated(reader io.Reader, limit int) ([]byte, bool, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if len(raw) > limit {
		return raw[:limit], true, err
	}
	return raw, false, err
}

func closeBodyOnCancel(ctx context.Context, body io.ReadCloser) func() {
	stop := context.AfterFunc(ctx, func() {
		_ = body.Close()
	})
	return func() {
		stop()
		_ = body.Close()
	}
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		character := name[i]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character)) {
			continue
		}
		return false
	}
	return true
}

func validHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		character := value[i]
		if character == '\t' || (character >= 0x20 && character != 0x7f) {
			continue
		}
		return false
	}
	return true
}

func redactSecret(value, secret string) string {
	if secret == "" {
		return value
	}
	return strings.ReplaceAll(value, secret, "[REDACTED]")
}

func protocolError(format string, args ...any) error {
	return &ProviderProtocolError{Detail: fmt.Sprintf(format, args...)}
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

var (
	_ model.StreamingProvider = (*provider)(nil)
)

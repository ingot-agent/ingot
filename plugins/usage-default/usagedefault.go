// Package usagedefault implements model-aware input counting with configured
// provider/model routing and bounded in-memory caching.
package usagedefault

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sync"
	"unicode/utf8"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/usage"
)

const defaultCacheEntries = 1024

var (
	// ErrInvalidConfig indicates invalid routes, cache limits, or dependencies.
	ErrInvalidConfig = errors.New("invalid usage.default config")
	// ErrInvalidRequest indicates malformed model invocation data.
	ErrInvalidRequest = errors.New("invalid usage count request")
	// ErrUnsupportedModel aliases the SDK classification used when no route or
	// profile supports a resolved provider/model pair.
	ErrUnsupportedModel = usage.ErrUnsupportedModel
	// ErrCountFailed indicates that a selected profile could not produce a
	// valid count.
	ErrCountFailed = errors.New("usage count failed")
	// ErrClosed indicates that the component instance has been cleaned up.
	ErrClosed = errors.New("usage counter is closed")
)

// Config controls provider/model routes and the bounded result cache.
type Config struct {
	Routes       []Route `toml:"routes"`
	CacheEntries int     `toml:"cache_entries"`
}

// Route selects one built-in profile for a provider and full model regexp.
type Route struct {
	Provider     string `toml:"provider"`
	ModelPattern string `toml:"model_pattern"`
	Profile      string `toml:"profile"`
}

// Dependencies contains the request resolver used to materialize model
// runtime defaults.
type Dependencies struct {
	Resolver model.RequestResolver
}

// Exports contains the model input counter.
type Exports struct {
	Counter usage.Counter
}

type compiledRoute struct {
	provider string
	pattern  *regexp.Regexp
	profile  profile
}

type cacheEntry struct {
	key    string
	result usage.CountResult
}

type flight struct {
	done   chan struct{}
	result usage.CountResult
	err    error
}

type counter struct {
	resolver model.RequestResolver
	routes   []compiledRoute
	capacity int

	mu       sync.Mutex
	closed   bool
	cache    map[string]*list.Element
	recent   *list.List
	inflight map[string]*flight
}

// New validates all routes and constructs an independent counter instance.
func New(ctx context.Context, cfg Config, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, fmt.Errorf("construct usage.default: nil context: %w", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	if isNil(deps.Resolver) {
		return Exports{}, nil, fmt.Errorf("resolver dependency is required: %w", ErrInvalidConfig)
	}
	profiles, err := builtInProfiles()
	if err != nil {
		return Exports{}, nil, fmt.Errorf("initialize built-in profiles: %w: %w", ErrInvalidConfig, err)
	}
	routes, err := compileRoutes(cfg.Routes, profiles)
	if err != nil {
		return Exports{}, nil, err
	}
	capacity := cfg.CacheEntries
	if capacity < 0 {
		return Exports{}, nil, fmt.Errorf("cache_entries must not be negative: %w", ErrInvalidConfig)
	}
	if capacity == 0 {
		capacity = defaultCacheEntries
	}
	instance := newCounter(deps.Resolver, routes, capacity)
	cleanup := ingotabi.Cleanup(func(cleanupCtx context.Context) error {
		if cleanupCtx == nil {
			return errors.New("cleanup usage.default: nil context")
		}
		if err := cleanupCtx.Err(); err != nil {
			return err
		}
		instance.close()
		return nil
	})
	return Exports{Counter: instance}, cleanup, nil
}

func newCounter(resolver model.RequestResolver, routes []compiledRoute, capacity int) *counter {
	return &counter{
		resolver: resolver,
		routes:   routes,
		capacity: capacity,
		cache:    make(map[string]*list.Element, capacity),
		recent:   list.New(),
		inflight: make(map[string]*flight),
	}
}

func compileRoutes(routes []Route, profiles map[string]profile) ([]compiledRoute, error) {
	if len(routes) == 0 {
		return nil, fmt.Errorf("routes must not be empty: %w", ErrInvalidConfig)
	}
	result := make([]compiledRoute, len(routes))
	for i, route := range routes {
		if route.Provider == "" || route.ModelPattern == "" || route.Profile == "" {
			return nil, fmt.Errorf("routes[%d] requires provider, model_pattern, and profile: %w", i, ErrInvalidConfig)
		}
		if !utf8.ValidString(route.Provider) || !utf8.ValidString(route.ModelPattern) || !utf8.ValidString(route.Profile) {
			return nil, fmt.Errorf("routes[%d] contains invalid UTF-8: %w", i, ErrInvalidConfig)
		}
		selected, ok := profiles[route.Profile]
		if !ok {
			return nil, fmt.Errorf("routes[%d] profile %q is unknown: %w", i, route.Profile, ErrInvalidConfig)
		}
		if selected.Source() == "" || !utf8.ValidString(selected.Source()) || !validAccuracy(selected.Accuracy()) {
			return nil, fmt.Errorf("routes[%d] profile %q has invalid metadata: %w", i, route.Profile, ErrInvalidConfig)
		}
		pattern, err := regexp.Compile(route.ModelPattern)
		if err != nil {
			return nil, fmt.Errorf("routes[%d] model_pattern: %w: %w", i, ErrInvalidConfig, err)
		}
		result[i] = compiledRoute{provider: route.Provider, pattern: pattern, profile: selected}
	}
	return result, nil
}

func validAccuracy(value usage.Accuracy) bool {
	switch value {
	case usage.AccuracyExact, usage.AccuracyUpperBound, usage.AccuracyEstimate:
		return true
	default:
		return false
	}
}

func (c *counter) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	clear(c.cache)
	c.recent.Init()
}

func selectProfile(routes []compiledRoute, provider, modelName string) (profile, int, bool) {
	for i, route := range routes {
		if route.provider != provider {
			continue
		}
		match := route.pattern.FindStringIndex(modelName)
		if match != nil && match[0] == 0 && match[1] == len(modelName) {
			return route.profile, i, true
		}
	}
	return nil, -1, false
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ usage.Counter = (*counter)(nil)

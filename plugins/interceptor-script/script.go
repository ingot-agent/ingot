// Package script executes trusted external policy hooks as typed SDK interceptors.
package script

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/pipeline"
	"github.com/ingot-agent/sdk/tool"
)

const (
	defaultTimeoutSeconds = 10
	defaultMaxOutputBytes = 64 * 1024
)

var (
	// ErrInvalidConfig indicates invalid hook declarations.
	ErrInvalidConfig = errors.New("invalid interceptor.script config")
	// ErrHookFailed indicates hook launch, execution, output, or protocol failure.
	ErrHookFailed = errors.New("script hook failed")
	// ErrHookRejected indicates an explicit before-phase policy rejection.
	ErrHookRejected = errors.New("script hook rejected call")
	// ErrAfterHookFailed indicates that an after hook failed after downstream ran.
	ErrAfterHookFailed = errors.New("script after hook failed")
	// ErrCompletionUnknown indicates that downstream may already have side effects.
	ErrCompletionUnknown = errors.New("operation completion status unknown")
	// ErrProcessCleanup indicates failure to contain or reap a hook process.
	ErrProcessCleanup = errors.New("script hook process cleanup failed")
)

// Config declares script hooks in interceptor order.
type Config struct {
	Hooks []Hook `toml:"hooks"`
}

// Hook configures one target-specific executable policy hook.
type Hook struct {
	Name           string            `toml:"name"`
	Target         string            `toml:"target"`
	Executable     string            `toml:"executable"`
	Args           []string          `toml:"args"`
	TimeoutSeconds int               `toml:"timeout_seconds"`
	MaxOutputBytes int               `toml:"max_output_bytes"`
	Environment    map[string]string `toml:"environment"`
}

// Dependencies contains no consumed capabilities.
type Dependencies struct{}

// Exports contains independent interceptor collections for each target.
type Exports struct {
	ToolInterceptors   []tool.Interceptor
	ModelInterceptors  []model.Interceptor
	StreamInterceptors []model.StreamInterceptor
	AgentInterceptors  []agent.Interceptor
}

type normalizedHook struct {
	name        string
	target      string
	executable  string
	args        []string
	environment []string
	dir         string
	timeout     time.Duration
	maxOutput   int
}

// New validates hook declarations and exports target-specific wrappers in declaration order.
func New(ctx context.Context, cfg Config, _ Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, fmt.Errorf("construct interceptor.script: %w", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	seen := make(map[string]struct{}, len(cfg.Hooks))
	var exports Exports
	for i, candidate := range cfg.Hooks {
		hook, err := normalizeHook(candidate)
		if err != nil {
			return Exports{}, nil, fmt.Errorf("hooks[%d]: %w", i, err)
		}
		if _, exists := seen[hook.name]; exists {
			return Exports{}, nil, fmt.Errorf("hooks[%d] duplicate name %q: %w", i, hook.name, ErrInvalidConfig)
		}
		seen[hook.name] = struct{}{}
		switch hook.target {
		case "tool":
			exports.ToolInterceptors = append(exports.ToolInterceptors, &toolHook{hook: hook})
		case "model":
			exports.ModelInterceptors = append(exports.ModelInterceptors, &modelHook{hook: hook})
		case "model-stream":
			exports.StreamInterceptors = append(exports.StreamInterceptors, &streamHook{hook: hook})
		case "agent":
			exports.AgentInterceptors = append(exports.AgentInterceptors, &agentHook{hook: hook})
		}
	}
	return exports, nil, nil
}

func normalizeHook(cfg Hook) (normalizedHook, error) {
	if cfg.Name == "" || !utf8.ValidString(cfg.Name) {
		return normalizedHook{}, fmt.Errorf("name must be non-empty UTF-8: %w", ErrInvalidConfig)
	}
	switch cfg.Target {
	case "tool", "model", "model-stream", "agent":
	default:
		return normalizedHook{}, fmt.Errorf("target %q is unsupported: %w", cfg.Target, ErrInvalidConfig)
	}
	if !filepath.IsAbs(cfg.Executable) || strings.ContainsRune(cfg.Executable, 0) {
		return normalizedHook{}, fmt.Errorf("executable must be absolute: %w", ErrInvalidConfig)
	}
	info, err := os.Stat(cfg.Executable)
	if err != nil || !info.Mode().IsRegular() {
		return normalizedHook{}, fmt.Errorf("executable must be an existing regular file: %w", ErrInvalidConfig)
	}
	timeout := cfg.TimeoutSeconds
	if timeout == 0 {
		timeout = defaultTimeoutSeconds
	}
	if timeout < 1 {
		return normalizedHook{}, fmt.Errorf("timeout_seconds must be positive: %w", ErrInvalidConfig)
	}
	const maxTimeoutSeconds = int64((1<<63 - 1) / int64(time.Second))
	if int64(timeout) > maxTimeoutSeconds {
		return normalizedHook{}, fmt.Errorf("timeout_seconds is too large: %w", ErrInvalidConfig)
	}
	maxOutput := cfg.MaxOutputBytes
	if maxOutput == 0 {
		maxOutput = defaultMaxOutputBytes
	}
	if maxOutput < 1 {
		return normalizedHook{}, fmt.Errorf("max_output_bytes must be positive: %w", ErrInvalidConfig)
	}
	args := append([]string(nil), cfg.Args...)
	for i, arg := range args {
		if strings.ContainsRune(arg, 0) {
			return normalizedHook{}, fmt.Errorf("args[%d] contains NUL: %w", i, ErrInvalidConfig)
		}
	}
	keys := make([]string, 0, len(cfg.Environment))
	seenKeys := make(map[string]string, len(cfg.Environment))
	for key, value := range cfg.Environment {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return normalizedHook{}, fmt.Errorf("environment contains invalid key or value: %w", ErrInvalidConfig)
		}
		folded := key
		if runtime.GOOS == "windows" {
			folded = strings.ToLower(key)
		}
		if previous, exists := seenKeys[folded]; exists {
			return normalizedHook{}, fmt.Errorf("environment key %q duplicates %q case-insensitively: %w", key, previous, ErrInvalidConfig)
		}
		seenKeys[folded] = key
		keys = append(keys, key)
	}
	sort.Strings(keys)
	environment := make([]string, 0, len(keys))
	for _, key := range keys {
		environment = append(environment, key+"="+cfg.Environment[key])
	}
	return normalizedHook{
		name: cfg.Name, target: cfg.Target, executable: cfg.Executable, args: args,
		environment: environment, dir: filepath.Dir(cfg.Executable),
		timeout: time.Duration(timeout) * time.Second, maxOutput: maxOutput,
	}, nil
}

type toolHook struct{ hook normalizedHook }
type modelHook struct{ hook normalizedHook }
type streamHook struct{ hook normalizedHook }
type agentHook struct{ hook normalizedHook }

func (h *toolHook) Invoke(ctx context.Context, request tool.Call, next pipeline.Next[tool.Call, tool.Result]) (tool.Result, error) {
	if next == nil {
		return tool.Result{}, errors.New("script tool interceptor: nil next")
	}
	projection, err := projectToolRequest(request)
	if err != nil {
		return tool.Result{}, err
	}
	if err := runBefore(ctx, h.hook, projection); err != nil {
		return tool.Result{}, err
	}
	response, downstreamErr := next(ctx, request)
	projected, projectionErr := projectToolResponse(response)
	return finishAfter(ctx, h.hook, projection, projected, response, downstreamErr, projectionErr)
}

func (h *modelHook) Invoke(ctx context.Context, request model.Request, next pipeline.Next[model.Request, model.Response]) (model.Response, error) {
	if next == nil {
		return model.Response{}, errors.New("script model interceptor: nil next")
	}
	projection, err := projectModelRequest(request)
	if err != nil {
		return model.Response{}, err
	}
	if err := runBefore(ctx, h.hook, projection); err != nil {
		return model.Response{}, err
	}
	response, downstreamErr := next(ctx, request)
	projected, projectionErr := projectModelResponse(response)
	return finishAfter(ctx, h.hook, projection, projected, response, downstreamErr, projectionErr)
}

func (h *streamHook) InvokeStream(ctx context.Context, request model.Request, handler model.StreamHandler, next model.StreamNext) (model.Response, error) {
	if next == nil {
		return model.Response{}, errors.New("script stream interceptor: nil next")
	}
	projection, err := projectModelRequest(request)
	if err != nil {
		return model.Response{}, err
	}
	if err := runBefore(ctx, h.hook, projection); err != nil {
		return model.Response{}, err
	}
	response, downstreamErr := next(ctx, request, handler)
	projected, projectionErr := projectModelResponse(response)
	return finishAfter(ctx, h.hook, projection, projected, response, downstreamErr, projectionErr)
}

func (h *agentHook) Invoke(ctx context.Context, request agent.Turn, next pipeline.Next[agent.Turn, agent.Result]) (agent.Result, error) {
	if next == nil {
		return agent.Result{}, errors.New("script agent interceptor: nil next")
	}
	projection, err := projectAgentRequest(request)
	if err != nil {
		return agent.Result{}, err
	}
	if err := runBefore(ctx, h.hook, projection); err != nil {
		return agent.Result{}, err
	}
	response, downstreamErr := next(ctx, request)
	projected, projectionErr := projectAgentResponse(response)
	return finishAfter(ctx, h.hook, projection, projected, response, downstreamErr, projectionErr)
}

var (
	_ tool.Interceptor        = (*toolHook)(nil)
	_ model.Interceptor       = (*modelHook)(nil)
	_ model.StreamInterceptor = (*streamHook)(nil)
	_ agent.Interceptor       = (*agentHook)(nil)
)

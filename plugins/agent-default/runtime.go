// Package agentdefault implements the standard session-aware model and tool loop.
package agentdefault

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"unicode/utf8"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/asset"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/prompt"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

const defaultMaxToolRounds = 8

var (
	// ErrInvalidConfig indicates invalid limits or dependency combinations.
	ErrInvalidConfig = errors.New("invalid agent.default config")
	// ErrInvalidTurn indicates an empty session ID or invalid user input.
	ErrInvalidTurn = errors.New("invalid agent turn")
	// ErrMaxToolRounds indicates that the model requested another tool round beyond the configured bound.
	ErrMaxToolRounds = errors.New("maximum tool rounds exceeded")
	// ErrInvalidModelMessage indicates an invalid assistant response.
	ErrInvalidModelMessage = errors.New("invalid model message")
	// ErrUnsupportedEntryVersion indicates an unknown agent.message payload version.
	ErrUnsupportedEntryVersion = errors.New("unsupported agent entry version")
	// ErrCorruptHistory indicates invalid agent-owned message ordering or payload data.
	ErrCorruptHistory = errors.New("corrupt agent history")
)

// Config controls model selection, generation, and tool behavior.
type Config struct {
	Provider      string   `toml:"provider"`
	Model         string   `toml:"model"`
	Temperature   *float64 `toml:"temperature"`
	MaxTokens     *int     `toml:"max_tokens"`
	MaxToolRounds int      `toml:"max_tool_rounds"`
	// Deprecated: retained for config compatibility and ignored. Use the
	// Streaming export's Stream method to request incremental output.
	Streaming     bool   `toml:"streaming"`
	ToolErrorMode string `toml:"tool_error_mode"`
}

// Dependencies contains the runtime chokepoints used by an agent turn.
type Dependencies struct {
	Model        model.Runtime
	Streaming    ingotabi.Optional[model.StreamingRuntime]
	Tools        tool.Runtime
	Store        session.Store
	Assets       asset.Store
	Prompt       prompt.Renderer
	Compactor    ingotabi.Optional[contextwindow.Compactor]
	Interceptors []agent.Interceptor
}

// Exports contains independent turn, output streaming, and history capabilities.
type Exports struct {
	Runtime   agent.Runtime
	Streaming agent.StreamingRuntime
	History   agent.History
}

type runtime struct {
	model         model.Runtime
	streaming     ingotabi.Optional[model.StreamingRuntime]
	tools         tool.Runtime
	store         session.Store
	assets        asset.Store
	prompt        prompt.Renderer
	compactor     ingotabi.Optional[contextwindow.Compactor]
	interceptors  []agent.Interceptor
	gates         *gateManager
	provider      string
	modelName     string
	temperature   *float64
	maxTokens     *int
	maxToolRounds int
	toolErrorMode string
}

// New validates immutable dependencies and creates an independent runtime.
func New(ctx context.Context, cfg Config, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, fmt.Errorf("construct agent.default: %w", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	if isNil(deps.Model) || isNil(deps.Tools) || isNil(deps.Store) || isNil(deps.Assets) || isNil(deps.Prompt) {
		return Exports{}, nil, fmt.Errorf("required dependency is nil: %w", ErrInvalidConfig)
	}
	if deps.Streaming.Valid && isNil(deps.Streaming.Value) {
		return Exports{}, nil, fmt.Errorf("streaming dependency is typed nil: %w", ErrInvalidConfig)
	}
	if deps.Compactor.Valid && isNil(deps.Compactor.Value) {
		return Exports{}, nil, fmt.Errorf("compactor dependency is typed nil: %w", ErrInvalidConfig)
	}
	if cfg.Temperature != nil && (math.IsNaN(*cfg.Temperature) || math.IsInf(*cfg.Temperature, 0) || *cfg.Temperature < 0 || *cfg.Temperature > 2) {
		return Exports{}, nil, fmt.Errorf("temperature must be in [0,2]: %w", ErrInvalidConfig)
	}
	if cfg.MaxTokens != nil && *cfg.MaxTokens < 1 {
		return Exports{}, nil, fmt.Errorf("max_tokens must be positive: %w", ErrInvalidConfig)
	}
	maxRounds := cfg.MaxToolRounds
	if maxRounds == 0 {
		maxRounds = defaultMaxToolRounds
	}
	if maxRounds < 1 {
		return Exports{}, nil, fmt.Errorf("max_tool_rounds must be positive: %w", ErrInvalidConfig)
	}
	mode := cfg.ToolErrorMode
	if mode == "" {
		mode = "result"
	}
	if mode != "result" && mode != "fail" {
		return Exports{}, nil, fmt.Errorf("tool_error_mode must be result or fail: %w", ErrInvalidConfig)
	}
	interceptors := make([]agent.Interceptor, len(deps.Interceptors))
	for i, interceptor := range deps.Interceptors {
		if isNil(interceptor) {
			return Exports{}, nil, fmt.Errorf("interceptors[%d] is nil: %w", i, ErrInvalidConfig)
		}
		interceptors[i] = interceptor
	}
	instance := &runtime{
		model: deps.Model, streaming: deps.Streaming, tools: deps.Tools, store: deps.Store, assets: deps.Assets,
		prompt: deps.Prompt, compactor: deps.Compactor, interceptors: interceptors,
		gates: newGateManager(), provider: cfg.Provider, modelName: cfg.Model,
		temperature: copyFloat(cfg.Temperature), maxTokens: copyInt(cfg.MaxTokens),
		maxToolRounds: maxRounds, toolErrorMode: mode,
	}
	return Exports{Runtime: instance, Streaming: instance, History: instance}, nil, nil
}

// Load returns a validated, caller-owned snapshot of one session's persisted
// model messages without performing interrupted-round recovery or other
// writes.
func (r *runtime) Load(ctx context.Context, sessionID session.ID) ([]model.Message, error) {
	if ctx == nil || sessionID == "" || !utf8.ValidString(string(sessionID)) {
		return nil, ErrInvalidTurn
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	release, err := r.gates.acquire(ctx, string(sessionID))
	if err != nil {
		return nil, err
	}
	defer release()
	messages, err := r.loadHistory(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return cloneMessages(messages), nil
}

func (r *runtime) Run(ctx context.Context, turn agent.Turn) (agent.Result, error) {
	return r.execute(ctx, turn, nil)
}

func validateAssistant(message model.Message) error {
	if message.Role != model.RoleAssistant {
		return ErrInvalidModelMessage
	}
	if err := content.Validate(message.Content); err != nil {
		return fmt.Errorf("assistant content: %w: %w", ErrInvalidModelMessage, err)
	}
	seen := make(map[string]struct{}, len(message.ToolCalls))
	for i, call := range message.ToolCalls {
		if call.ID == "" || call.Name == "" || !utf8.ValidString(call.ID) || !utf8.ValidString(call.Name) || !json.Valid(call.Arguments) {
			return fmt.Errorf("tool_calls[%d]: %w", i, ErrInvalidModelMessage)
		}
		if _, duplicate := seen[call.ID]; duplicate {
			return fmt.Errorf("duplicate tool call id %q: %w", call.ID, ErrInvalidModelMessage)
		}
		seen[call.ID] = struct{}{}
	}
	return nil
}

func (r *runtime) materializeContent(ctx context.Context, value content.Content) (content.Content, error) {
	result := content.Clone(value)
	for i := range result {
		part := &result[i]
		if part.Kind == content.KindText || part.Media.Source.Kind != content.SourceInline {
			continue
		}
		data := part.Media.Source.Data
		reference, info, err := r.assets.Put(ctx, asset.PutRequest{Body: bytes.NewReader(data), Size: uint64(len(data))})
		if err != nil {
			return nil, fmt.Errorf("persist content part %d asset: %w", i, err)
		}
		if reference.ID == "" || info.Size != uint64(len(data)) {
			return nil, fmt.Errorf("persist content part %d asset returned invalid reference or size", i)
		}
		part.Media.Source = content.Source{Kind: content.SourceAsset, Asset: reference}
	}
	return result, nil
}

func safeToolError(err error) string {
	switch {
	case errors.Is(err, tool.ErrNotFound):
		return "tool error [not_found]: requested tool is unavailable"
	case errors.Is(err, tool.ErrInvalidArguments):
		return "tool error [invalid_arguments]: tool arguments were rejected"
	default:
		return "tool error [execution_failed]: tool execution failed"
	}
}

var _ agent.Runtime = (*runtime)(nil)
var _ agent.StreamingRuntime = (*runtime)(nil)
var _ agent.History = (*runtime)(nil)

func copyFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copyInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
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

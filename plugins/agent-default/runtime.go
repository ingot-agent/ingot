// Package agentdefault implements the standard session-aware model and tool loop.
package agentdefault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"unicode/utf8"

	"github.com/ingot-agent/sdk"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/pipeline"
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

// Config controls model selection, generation, streaming, and tool behavior.
type Config struct {
	Provider      string   `toml:"provider"`
	Model         string   `toml:"model"`
	Temperature   *float64 `toml:"temperature"`
	MaxTokens     *int     `toml:"max_tokens"`
	MaxToolRounds int      `toml:"max_tool_rounds"`
	Streaming     bool     `toml:"streaming"`
	ToolErrorMode string   `toml:"tool_error_mode"`
}

// Dependencies contains the runtime chokepoints used by an agent turn.
type Dependencies struct {
	Model        model.Runtime
	Streaming    sdk.Optional[model.StreamingRuntime]
	Tools        tool.Runtime
	Store        session.Store
	Prompt       prompt.Renderer
	Compactor    sdk.Optional[contextwindow.Compactor]
	Interaction  sdk.Optional[interaction.Channel]
	Interceptors []agent.Interceptor
}

// Exports contains the agent runtime.
type Exports struct {
	Runtime agent.Runtime
}

type runtime struct {
	model         model.Runtime
	streaming     sdk.Optional[model.StreamingRuntime]
	tools         tool.Runtime
	store         session.Store
	prompt        prompt.Renderer
	compactor     sdk.Optional[contextwindow.Compactor]
	interaction   sdk.Optional[interaction.Channel]
	interceptors  []agent.Interceptor
	gates         *gateManager
	provider      string
	modelName     string
	temperature   *float64
	maxTokens     *int
	maxToolRounds int
	streamEnabled bool
	toolErrorMode string
}

// New validates immutable dependencies and creates an independent runtime.
func New(ctx context.Context, cfg Config, deps Dependencies) (Exports, sdk.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, fmt.Errorf("construct agent.default: %w", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	if isNil(deps.Model) || isNil(deps.Tools) || isNil(deps.Store) || isNil(deps.Prompt) {
		return Exports{}, nil, fmt.Errorf("required dependency is nil: %w", ErrInvalidConfig)
	}
	if deps.Streaming.Valid && isNil(deps.Streaming.Value) {
		return Exports{}, nil, fmt.Errorf("streaming dependency is typed nil: %w", ErrInvalidConfig)
	}
	if cfg.Streaming && !deps.Streaming.Valid {
		return Exports{}, nil, fmt.Errorf("streaming=true without model.StreamingRuntime: %w", ErrInvalidConfig)
	}
	if deps.Compactor.Valid && isNil(deps.Compactor.Value) {
		return Exports{}, nil, fmt.Errorf("compactor dependency is typed nil: %w", ErrInvalidConfig)
	}
	if deps.Interaction.Valid && isNil(deps.Interaction.Value) {
		return Exports{}, nil, fmt.Errorf("interaction dependency is typed nil: %w", ErrInvalidConfig)
	}
	if cfg.Temperature != nil && (*cfg.Temperature < 0 || *cfg.Temperature > 2) {
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
		model: deps.Model, streaming: deps.Streaming, tools: deps.Tools, store: deps.Store,
		prompt: deps.Prompt, compactor: deps.Compactor, interaction: deps.Interaction, interceptors: interceptors,
		gates: newGateManager(), provider: cfg.Provider, modelName: cfg.Model,
		temperature: copyFloat(cfg.Temperature), maxTokens: copyInt(cfg.MaxTokens),
		maxToolRounds: maxRounds, streamEnabled: cfg.Streaming, toolErrorMode: mode,
	}
	return Exports{Runtime: instance}, nil, nil
}

func (r *runtime) Run(ctx context.Context, turn agent.Turn) (agent.Result, error) {
	if ctx == nil {
		return agent.Result{}, fmt.Errorf("run agent: nil context: %w", ErrInvalidTurn)
	}
	if turn.SessionID == "" || !utf8.ValidString(string(turn.SessionID)) || !utf8.ValidString(turn.Input) {
		return agent.Result{}, ErrInvalidTurn
	}
	if err := ctx.Err(); err != nil {
		return agent.Result{}, err
	}
	release, err := r.gates.acquire(ctx, string(turn.SessionID))
	if err != nil {
		return agent.Result{}, err
	}
	defer release()

	originalSessionID := turn.SessionID
	terminal := func(callCtx context.Context, selected agent.Turn) (agent.Result, error) {
		if selected.SessionID != originalSessionID {
			return agent.Result{}, fmt.Errorf("agent interceptor changed session id from %q to %q: %w", originalSessionID, selected.SessionID, ErrInvalidTurn)
		}
		return r.runTurn(callCtx, selected)
	}
	next := pipeline.Compose[agent.Turn, agent.Result](terminal, r.interceptors...)
	return next(ctx, turn)
}

func (r *runtime) runTurn(ctx context.Context, turn agent.Turn) (agent.Result, error) {
	history, err := r.loadHistory(ctx, turn.SessionID)
	if err != nil {
		return agent.Result{}, err
	}
	history, err = r.recoverTrailingRound(ctx, turn.SessionID, history)
	if err != nil {
		return agent.Result{}, err
	}
	user := model.Message{Role: model.RoleUser, Content: turn.Input}
	if err := r.appendMessage(ctx, turn.SessionID, user); err != nil {
		return agent.Result{}, fmt.Errorf("append user message: %w", err)
	}
	messages, err := r.prompt.Render(ctx, prompt.Request{SessionID: turn.SessionID, Input: turn.Input, History: cloneMessages(history)})
	if err != nil {
		return agent.Result{}, fmt.Errorf("render prompt: %w", err)
	}
	messages = cloneMessages(messages)
	definitions := cloneDefinitions(r.tools.Definitions())
	rounds := 0
	for {
		request := model.Request{
			Provider: r.provider, Model: r.modelName, Messages: cloneMessages(messages), Tools: cloneDefinitions(definitions),
			Temperature: copyFloat(r.temperature), MaxTokens: copyInt(r.maxTokens),
		}
		request, err = r.compactRequest(ctx, turn.SessionID, request)
		if err != nil {
			return agent.Result{}, err
		}
		response, err := r.callModel(ctx, request)
		if err != nil {
			return agent.Result{}, err
		}
		if err := validateAssistant(response.Message); err != nil {
			return agent.Result{}, err
		}
		if err := r.appendMessage(ctx, turn.SessionID, response.Message); err != nil {
			return agent.Result{}, fmt.Errorf("append assistant message: %w", err)
		}
		messages = append(messages, cloneMessage(response.Message))
		if len(response.Message.ToolCalls) == 0 {
			return agent.Result{Output: response.Message.Content}, nil
		}
		rounds++
		if rounds > r.maxToolRounds {
			return agent.Result{}, ErrMaxToolRounds
		}
		for _, call := range response.Message.ToolCalls {
			if err := r.render(ctx, interaction.ToolCallEvent{Call: cloneCall(call)}); err != nil {
				return agent.Result{}, err
			}
			result, callErr := r.tools.Call(ctx, cloneCall(call))
			if callErr != nil {
				if errors.Is(callErr, context.Canceled) || errors.Is(callErr, context.DeadlineExceeded) {
					return agent.Result{}, callErr
				}
				if r.toolErrorMode == "fail" {
					return agent.Result{}, fmt.Errorf("tool %q call %q: %w", call.Name, call.ID, callErr)
				}
				result = tool.Result{Content: safeToolError(callErr)}
			}
			if !utf8.ValidString(result.Content) {
				return agent.Result{}, fmt.Errorf("tool %q returned invalid UTF-8", call.Name)
			}
			if err := r.render(ctx, interaction.ToolResultEvent{Call: cloneCall(call), Result: result}); err != nil {
				return agent.Result{}, err
			}
			message := model.Message{Role: model.RoleTool, Content: result.Content, ToolCallID: call.ID}
			if err := r.appendMessage(ctx, turn.SessionID, message); err != nil {
				return agent.Result{}, fmt.Errorf("append tool result for %q: %w", call.ID, err)
			}
			messages = append(messages, message)
		}
	}
}

func (r *runtime) compactRequest(ctx context.Context, sessionID session.ID, request model.Request) (model.Request, error) {
	if !r.compactor.Valid {
		return request, nil
	}
	result, err := r.compactor.Value.Compact(ctx, contextwindow.CompactionRequest{
		SessionID:  sessionID,
		Invocation: cloneModelRequest(request),
	})
	if err != nil {
		return model.Request{}, fmt.Errorf("compact model context: %w", err)
	}
	request.Messages = cloneMessages(result.Messages)
	return request, nil
}

func (r *runtime) callModel(ctx context.Context, request model.Request) (model.Response, error) {
	if !r.streamEnabled {
		response, err := r.model.Complete(ctx, request)
		if err != nil {
			return model.Response{}, fmt.Errorf("complete model: %w", err)
		}
		if response.Message.Content != "" {
			if err := r.render(ctx, interaction.TextEvent{Text: response.Message.Content}); err != nil {
				return model.Response{}, err
			}
		}
		return response, nil
	}
	response, err := r.streaming.Value.Stream(ctx, request, func(chunk model.StreamChunk) error {
		if chunk.TextDelta == "" {
			return nil
		}
		return r.render(ctx, interaction.TextEvent{Text: chunk.TextDelta})
	})
	if err != nil {
		return model.Response{}, fmt.Errorf("stream model: %w", err)
	}
	return response, nil
}

func (r *runtime) render(ctx context.Context, event interaction.Event) error {
	if !r.interaction.Valid {
		return nil
	}
	if err := r.interaction.Value.Render(ctx, event); err != nil {
		return fmt.Errorf("render interaction event: %w", err)
	}
	return nil
}

func validateAssistant(message model.Message) error {
	if message.Role != model.RoleAssistant || !utf8.ValidString(message.Content) {
		return ErrInvalidModelMessage
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

var _ agent.Runtime = (*runtime)(nil)

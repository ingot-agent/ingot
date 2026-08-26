// Package modelruntime implements named provider selection and the standard
// complete and streaming interceptor chokepoints.
package modelruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"unicode/utf8"

	"github.com/ingot-agent/sdk"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/pipeline"
	"github.com/ingot-agent/sdk/tool"
)

var (
	// ErrInvalidConfig indicates invalid defaults or dependency collections.
	ErrInvalidConfig = errors.New("invalid model.runtime config")
	// ErrInvalidResponse indicates invalid aggregate data returned by a provider or interceptor.
	ErrInvalidResponse = errors.New("invalid model response")
)

// Config selects defaults used when a request leaves a field empty.
type Config struct {
	DefaultProvider string `toml:"default_provider"`
	DefaultModel    string `toml:"default_model"`
}

// Dependencies contains providers and the independent complete/stream chains.
type Dependencies struct {
	Providers          []sdk.Named[model.Provider]
	Interceptors       []model.Interceptor
	StreamInterceptors []model.StreamInterceptor
}

// Exports contains the complete and streaming runtimes.
type Exports struct {
	Runtime   model.Runtime
	Streaming model.StreamingRuntime
}

type runtime struct {
	providers          map[string]model.Provider
	defaultProvider    string
	defaultModel       string
	interceptors       []model.Interceptor
	streamInterceptors []model.StreamInterceptor
}

// New snapshots providers and composes immutable runtime state.
func New(ctx context.Context, cfg Config, deps Dependencies) (Exports, sdk.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, fmt.Errorf("construct model.runtime: %w", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	if len(deps.Providers) == 0 {
		return Exports{}, nil, fmt.Errorf("providers must not be empty: %w", ErrInvalidConfig)
	}
	if err := sdk.CheckUniqueNames(deps.Providers); err != nil {
		return Exports{}, nil, fmt.Errorf("providers: %w: %w", ErrInvalidConfig, err)
	}
	providers := make(map[string]model.Provider, len(deps.Providers))
	for i, named := range deps.Providers {
		if isNil(named.Value) {
			return Exports{}, nil, fmt.Errorf("providers[%d] is nil: %w", i, ErrInvalidConfig)
		}
		providers[named.Name] = named.Value
	}
	defaultProvider := cfg.DefaultProvider
	if defaultProvider == "" {
		if len(deps.Providers) != 1 {
			return Exports{}, nil, fmt.Errorf("default_provider is required with multiple providers: %w", ErrInvalidConfig)
		}
		defaultProvider = deps.Providers[0].Name
	} else if _, ok := providers[defaultProvider]; !ok {
		return Exports{}, nil, fmt.Errorf("default provider %q: %w: %w", defaultProvider, model.ErrProviderNotFound, ErrInvalidConfig)
	}

	interceptors := make([]model.Interceptor, len(deps.Interceptors))
	for i, interceptor := range deps.Interceptors {
		if isNil(interceptor) {
			return Exports{}, nil, fmt.Errorf("interceptors[%d] is nil: %w", i, ErrInvalidConfig)
		}
		interceptors[i] = interceptor
	}
	streamInterceptors := make([]model.StreamInterceptor, len(deps.StreamInterceptors))
	for i, interceptor := range deps.StreamInterceptors {
		if isNil(interceptor) {
			return Exports{}, nil, fmt.Errorf("stream_interceptors[%d] is nil: %w", i, ErrInvalidConfig)
		}
		streamInterceptors[i] = interceptor
	}

	instance := &runtime{
		providers: providers, defaultProvider: defaultProvider, defaultModel: cfg.DefaultModel,
		interceptors: interceptors, streamInterceptors: streamInterceptors,
	}
	return Exports{Runtime: instance, Streaming: instance}, nil, nil
}

func (r *runtime) Complete(ctx context.Context, request model.Request) (model.Response, error) {
	if ctx == nil {
		return model.Response{}, errors.New("model runtime: nil context")
	}
	if err := ctx.Err(); err != nil {
		return model.Response{}, err
	}
	owned := cloneRequest(request)
	r.applyDefaults(&owned)
	terminal := func(callCtx context.Context, selected model.Request) (model.Response, error) {
		provider, err := r.selectProvider(selected)
		if err != nil {
			return model.Response{}, err
		}
		response, err := provider.Complete(callCtx, cloneRequest(selected))
		if err != nil {
			return cloneResponse(response), err
		}
		response.Provider = selected.Provider
		if response.Model == "" {
			response.Model = selected.Model
		}
		if err := validateResponse(response); err != nil {
			return model.Response{}, err
		}
		return cloneResponse(response), nil
	}
	next := pipeline.Compose[model.Request, model.Response](terminal, r.interceptors...)
	response, err := next(ctx, owned)
	return cloneResponse(response), err
}

func (r *runtime) Stream(ctx context.Context, request model.Request, handler model.StreamHandler) (model.Response, error) {
	if ctx == nil {
		return model.Response{}, errors.New("model streaming runtime: nil context")
	}
	if err := ctx.Err(); err != nil {
		return model.Response{}, err
	}
	if handler == nil {
		return model.Response{}, errors.New("model streaming runtime: nil handler")
	}
	owned := cloneRequest(request)
	r.applyDefaults(&owned)
	terminal := model.StreamNext(func(callCtx context.Context, selected model.Request, selectedHandler model.StreamHandler) (model.Response, error) {
		provider, err := r.selectProvider(selected)
		if err != nil {
			return model.Response{}, err
		}
		streaming, ok := provider.(model.StreamingProvider)
		if !ok || isNil(streaming) {
			return model.Response{}, fmt.Errorf("provider %q: %w", selected.Provider, model.ErrStreamingUnsupported)
		}
		response, err := streaming.Stream(callCtx, cloneRequest(selected), selectedHandler)
		if err != nil {
			return cloneResponse(response), err
		}
		response.Provider = selected.Provider
		if response.Model == "" {
			response.Model = selected.Model
		}
		if err := validateResponse(response); err != nil {
			return model.Response{}, err
		}
		return cloneResponse(response), nil
	})
	next := terminal
	for i := len(r.streamInterceptors) - 1; i >= 0; i-- {
		interceptor := r.streamInterceptors[i]
		following := next
		next = func(callCtx context.Context, selected model.Request, selectedHandler model.StreamHandler) (model.Response, error) {
			return interceptor.InvokeStream(callCtx, selected, selectedHandler, following)
		}
	}
	response, err := next(ctx, owned, handler)
	return cloneResponse(response), err
}

func (r *runtime) applyDefaults(request *model.Request) {
	if request.Provider == "" {
		request.Provider = r.defaultProvider
	}
	if request.Model == "" {
		request.Model = r.defaultModel
	}
}

func (r *runtime) selectProvider(request model.Request) (model.Provider, error) {
	if request.Provider == "" {
		return nil, fmt.Errorf("empty provider: %w", model.ErrProviderNotFound)
	}
	provider, ok := r.providers[request.Provider]
	if !ok {
		return nil, fmt.Errorf("provider %q: %w", request.Provider, model.ErrProviderNotFound)
	}
	if request.Model == "" {
		return nil, fmt.Errorf("empty model for provider %q: set default_model in [plugins.model.runtime] or model in [plugins.agent.default]: %w", request.Provider, model.ErrModelNotFound)
	}
	return provider, nil
}

func validateResponse(response model.Response) error {
	if response.Message.Role != model.RoleAssistant || response.Provider == "" || response.Model == "" {
		return fmt.Errorf("response requires assistant role, provider, and model: %w", ErrInvalidResponse)
	}
	if !utf8.ValidString(response.Provider) || !utf8.ValidString(response.Model) ||
		!utf8.ValidString(response.FinishReason) || !utf8.ValidString(response.Message.Content) ||
		!utf8.ValidString(response.Message.Name) || !utf8.ValidString(response.Message.ToolCallID) {
		return fmt.Errorf("response contains invalid UTF-8: %w", ErrInvalidResponse)
	}
	if response.Usage.InputTokens < 0 || response.Usage.OutputTokens < 0 || response.Usage.TotalTokens < 0 {
		return fmt.Errorf("response usage is negative: %w", ErrInvalidResponse)
	}
	if response.Usage.TotalTokens != response.Usage.InputTokens+response.Usage.OutputTokens {
		return fmt.Errorf("response usage total is inconsistent: %w", ErrInvalidResponse)
	}
	for i, call := range response.Message.ToolCalls {
		if call.ID == "" || call.Name == "" || !utf8.ValidString(call.ID) || !utf8.ValidString(call.Name) ||
			!utf8.Valid(call.Arguments) || !json.Valid(call.Arguments) {
			return fmt.Errorf("response tool_calls[%d] is invalid: %w", i, ErrInvalidResponse)
		}
	}
	return nil
}

func cloneRequest(request model.Request) model.Request {
	request.Messages = cloneMessages(request.Messages)
	if request.Tools != nil {
		tools := make([]tool.Definition, len(request.Tools))
		for i, definition := range request.Tools {
			tools[i] = tool.Definition{Name: definition.Name, Description: definition.Description, InputSchema: cloneRawMessage(definition.InputSchema)}
		}
		request.Tools = tools
	}
	if request.Stop != nil {
		request.Stop = append(make([]string, 0, len(request.Stop)), request.Stop...)
	}
	if request.Temperature != nil {
		value := *request.Temperature
		request.Temperature = &value
	}
	if request.MaxTokens != nil {
		value := *request.MaxTokens
		request.MaxTokens = &value
	}
	return request
}

func cloneResponse(response model.Response) model.Response {
	response.Message = cloneMessage(response.Message)
	return response
}

func cloneMessages(messages []model.Message) []model.Message {
	if messages == nil {
		return nil
	}
	result := make([]model.Message, len(messages))
	for i, message := range messages {
		result[i] = cloneMessage(message)
	}
	return result
}

func cloneMessage(message model.Message) model.Message {
	calls := message.ToolCalls
	if calls == nil {
		return message
	}
	message.ToolCalls = make([]tool.Call, len(calls))
	for i, call := range calls {
		message.ToolCalls[i] = tool.Call{ID: call.ID, Name: call.Name, Arguments: cloneRawMessage(call.Arguments)}
	}
	return message
}

func cloneRawMessage(value json.RawMessage) json.RawMessage {
	if value == nil {
		return nil
	}
	result := make(json.RawMessage, len(value))
	copy(result, value)
	return result
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
	_ model.Runtime          = (*runtime)(nil)
	_ model.StreamingRuntime = (*runtime)(nil)
)

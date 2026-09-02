// Package modelruntime implements named provider selection and the standard
// complete and streaming interceptor chokepoints.
package modelruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"unicode/utf8"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/content"
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
	Providers          []ingotabi.Named[model.Provider]
	Interceptors       []model.Interceptor
	StreamInterceptors []model.StreamInterceptor
}

// Exports contains the complete and streaming runtimes, plus the read-only
// request resolver used by components that need the materialized provider and
// model selection before invocation.
type Exports struct {
	Runtime   model.Runtime
	Streaming model.StreamingRuntime
	Resolver  model.RequestResolver
}

type runtime struct {
	providers          map[string]model.Provider
	defaultProvider    string
	defaultModel       string
	interceptors       []model.Interceptor
	streamInterceptors []model.StreamInterceptor
}

// New snapshots providers and composes immutable runtime state.
func New(ctx context.Context, cfg Config, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, fmt.Errorf("construct model.runtime: %w", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	if len(deps.Providers) == 0 {
		return Exports{}, nil, fmt.Errorf("providers must not be empty: %w", ErrInvalidConfig)
	}
	if err := ingotabi.CheckUniqueNames(deps.Providers); err != nil {
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
	return Exports{Runtime: instance, Streaming: instance, Resolver: instance}, nil, nil
}

// ResolveRequest returns a caller-owned request with provider and model
// defaults materialized. It validates the final selection without invoking a
// provider or running model interceptors.
func (r *runtime) ResolveRequest(ctx context.Context, request model.Request) (model.Request, error) {
	if ctx == nil {
		return model.Request{}, errors.New("model request resolver: nil context")
	}
	if err := ctx.Err(); err != nil {
		return model.Request{}, err
	}
	owned := cloneRequest(request)
	r.applyDefaults(&owned)
	if _, err := r.selectProvider(owned); err != nil {
		return model.Request{}, err
	}
	if err := validateRequest(owned); err != nil {
		return model.Request{}, err
	}
	return owned, nil
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
		if callCtx == nil {
			return model.Response{}, errors.New("model runtime interceptor supplied nil context")
		}
		if err := validateRequest(selected); err != nil {
			return model.Response{}, err
		}
		provider, err := r.selectProvider(selected)
		if err != nil {
			return model.Response{}, err
		}
		if err := callCtx.Err(); err != nil {
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
	if err == nil {
		if validationErr := validateResponse(response); validationErr != nil {
			return model.Response{}, validationErr
		}
	}
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
	stream := newStreamValidator(handler)
	terminal := model.StreamNext(func(callCtx context.Context, selected model.Request, selectedHandler model.StreamHandler) (model.Response, error) {
		if callCtx == nil {
			return model.Response{}, errors.New("model streaming interceptor supplied nil context")
		}
		if err := validateRequest(selected); err != nil {
			return model.Response{}, err
		}
		provider, err := r.selectProvider(selected)
		if err != nil {
			return model.Response{}, err
		}
		streaming, ok := provider.(model.StreamingProvider)
		if !ok || isNil(streaming) {
			return model.Response{}, fmt.Errorf("provider %q: %w", selected.Provider, model.ErrStreamingUnsupported)
		}
		if err := callCtx.Err(); err != nil {
			return model.Response{}, err
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
	response, err := next(ctx, owned, stream.handle)
	if stream.handlerErr != nil {
		return cloneResponse(response), stream.handlerErr
	}
	if err == nil {
		if validationErr := validateResponse(response); validationErr != nil {
			return model.Response{}, validationErr
		}
		if validationErr := stream.finish(response.Message.Content); validationErr != nil {
			return model.Response{}, validationErr
		}
	}
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
		!utf8.ValidString(response.FinishReason) ||
		!utf8.ValidString(response.Message.Name) || !utf8.ValidString(response.Message.ToolCallID) {
		return fmt.Errorf("response contains invalid UTF-8: %w", ErrInvalidResponse)
	}
	if err := content.Validate(response.Message.Content); err != nil {
		return fmt.Errorf("response message content: %w: %w", ErrInvalidResponse, err)
	}
	if !response.Usage.Reported && (response.Usage.InputTokens != 0 || response.Usage.OutputTokens != 0 || response.Usage.TotalTokens != 0) {
		return fmt.Errorf("response usage has counts without presence: %w", ErrInvalidResponse)
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

func validateRequest(request model.Request) error {
	if !utf8.ValidString(request.Provider) || !utf8.ValidString(request.Model) {
		return fmt.Errorf("request provider or model contains invalid UTF-8: %w", ErrInvalidResponse)
	}
	for i, message := range request.Messages {
		if !validRole(message.Role) || !utf8.ValidString(message.Name) || !utf8.ValidString(message.ToolCallID) {
			return fmt.Errorf("request messages[%d] has invalid fields: %w", i, content.ErrInvalidContent)
		}
		if err := content.Validate(message.Content); err != nil {
			return fmt.Errorf("request messages[%d] content: %w", i, err)
		}
	}
	return nil
}

func validRole(role model.Role) bool {
	return role == model.RoleSystem || role == model.RoleUser || role == model.RoleAssistant || role == model.RoleTool
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
	message.Content = content.Clone(message.Content)
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

func validContentKind(kind content.Kind) bool {
	return kind >= content.KindText && kind <= content.KindFile
}

type streamValidator struct {
	handler    model.StreamHandler
	handlerErr error
	content    streamPartValidator
	reasoning  streamPartValidator
}

// Content is accumulated for final-response validation. Reasoning has the same
// lifecycle checks but never retains text or contributes to canonical content.
type streamPartValidator struct {
	index      int
	transient  bool
	parts      content.Content
	active     bool
	activeKind content.Kind
	activeMIME string
	activeName string
	text       []byte
	data       []byte
}

func newStreamValidator(handler model.StreamHandler) *streamValidator {
	return &streamValidator{handler: handler, reasoning: streamPartValidator{transient: true}}
}

func (v *streamValidator) handle(event model.StreamEvent) error {
	if v.handlerErr != nil {
		return v.handlerErr
	}
	if err := v.validateEvent(event); err != nil {
		v.handlerErr = err
		return err
	}
	owned := event
	owned.DataDelta = slices.Clone(event.DataDelta)
	if err := v.handler(owned); err != nil {
		v.handlerErr = err
		return err
	}
	return nil
}

func (v *streamValidator) validateEvent(event model.StreamEvent) error {
	switch event.Semantic {
	case model.StreamSemanticContent:
		return v.content.validateEvent(event)
	case model.StreamSemanticReasoning:
		if event.Kind == model.StreamPartStart && event.PartKind != content.KindText {
			return fmt.Errorf("reasoning stream part must be text: %w", ErrInvalidResponse)
		}
		return v.reasoning.validateEvent(event)
	default:
		return fmt.Errorf("unknown stream semantic %d: %w", event.Semantic, ErrInvalidResponse)
	}
}

func (v *streamPartValidator) validateEvent(event model.StreamEvent) error {
	switch event.Kind {
	case model.StreamPartStart:
		if v.active || event.PartIndex != v.index || !validContentKind(event.PartKind) || event.TextDelta != "" || len(event.DataDelta) != 0 || !utf8.ValidString(event.Name) {
			return fmt.Errorf("invalid stream part start at index %d: %w", event.PartIndex, ErrInvalidResponse)
		}
		if event.PartKind == content.KindText && (event.MIMEType != "" || event.Name != "") {
			return fmt.Errorf("text stream part carries media metadata: %w", ErrInvalidResponse)
		}
		v.active = true
		v.activeKind = event.PartKind
		v.activeMIME = event.MIMEType
		v.activeName = event.Name
		v.text = nil
		v.data = nil
	case model.StreamPartDelta:
		if !v.active || event.PartIndex != v.index || event.PartKind != 0 || event.MIMEType != "" || event.Name != "" {
			return fmt.Errorf("invalid stream part delta at index %d: %w", event.PartIndex, ErrInvalidResponse)
		}
		if v.activeKind == content.KindText {
			if len(event.DataDelta) != 0 || !utf8.ValidString(event.TextDelta) {
				return fmt.Errorf("invalid text stream delta: %w", ErrInvalidResponse)
			}
			if !v.transient {
				v.text = append(v.text, event.TextDelta...)
			}
		} else {
			if event.TextDelta != "" {
				return fmt.Errorf("media stream delta carries text: %w", ErrInvalidResponse)
			}
			v.data = append(v.data, event.DataDelta...)
		}
	case model.StreamPartEnd:
		if !v.active || event.PartIndex != v.index || event.PartKind != 0 || event.MIMEType != "" || event.Name != "" || event.TextDelta != "" || len(event.DataDelta) != 0 {
			return fmt.Errorf("invalid stream part end at index %d: %w", event.PartIndex, ErrInvalidResponse)
		}
		if !v.transient {
			if v.activeKind == content.KindText {
				v.parts = append(v.parts, content.Text(string(v.text)))
			} else {
				v.parts = append(v.parts, content.Inline(v.activeKind, v.activeMIME, v.activeName, v.data))
			}
		}
		v.active = false
		v.index++
		v.text = nil
		v.data = nil
	default:
		return fmt.Errorf("unknown stream event kind %d: %w", event.Kind, ErrInvalidResponse)
	}
	return nil
}

func (v *streamValidator) finish(final content.Content) error {
	if v.content.active || v.reasoning.active {
		return fmt.Errorf("stream ended with an incomplete part: %w", ErrInvalidResponse)
	}
	if !contentEqual(v.content.parts, final) {
		return fmt.Errorf("stream events do not match final response content: %w", ErrInvalidResponse)
	}
	return nil
}

func contentEqual(left, right content.Content) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].Kind != right[i].Kind || left[i].Text != right[i].Text ||
			left[i].Media.MIMEType != right[i].Media.MIMEType || left[i].Media.Name != right[i].Media.Name ||
			left[i].Media.Source.Kind != right[i].Media.Source.Kind || left[i].Media.Source.URI != right[i].Media.Source.URI ||
			left[i].Media.Source.Asset != right[i].Media.Source.Asset || !bytes.Equal(left[i].Media.Source.Data, right[i].Media.Source.Data) {
			return false
		}
	}
	return true
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
	_ model.RequestResolver  = (*runtime)(nil)
)

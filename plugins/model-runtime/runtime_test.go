package modelruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ingot-agent/ingot-abi"
	modelruntime "github.com/ingot-agent/model-runtime"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/pipeline"
	"github.com/ingot-agent/sdk/tool"
)

type fakeProvider struct {
	request model.Request
	calls   int
}

type providerFunc func(context.Context, model.Request) (model.Response, error)

func (f providerFunc) Complete(ctx context.Context, request model.Request) (model.Response, error) {
	return f(ctx, request)
}

type countingProvider struct {
	calls atomic.Int32
}

func (p *countingProvider) Complete(context.Context, model.Request) (model.Response, error) {
	p.calls.Add(1)
	return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("ok")}}, nil
}

func (p *fakeProvider) Complete(_ context.Context, request model.Request) (model.Response, error) {
	p.calls++
	p.request = request
	if len(request.Messages) != 0 && len(request.Messages[0].ToolCalls) != 0 && len(request.Messages[0].ToolCalls[0].Arguments) != 0 {
		request.Messages[0].ToolCalls[0].Arguments[0] = 'X'
	}
	return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("ok")}}, nil
}

type interceptorFunc func(context.Context, model.Request, pipeline.Next[model.Request, model.Response]) (model.Response, error)

func (f interceptorFunc) Invoke(ctx context.Context, request model.Request, next pipeline.Next[model.Request, model.Response]) (model.Response, error) {
	return f(ctx, request, next)
}

type streamingProvider struct{ fakeProvider }

func (p *streamingProvider) Stream(ctx context.Context, request model.Request, handler model.StreamHandler) (model.Response, error) {
	p.request = request
	if err := handler(model.StreamEvent{Kind: model.StreamPartStart, PartIndex: 0, PartKind: content.KindText}); err != nil {
		return model.Response{}, err
	}
	if err := handler(model.StreamEvent{Kind: model.StreamPartDelta, PartIndex: 0, TextDelta: "chunk"}); err != nil {
		return model.Response{}, err
	}
	if err := handler(model.StreamEvent{Kind: model.StreamPartEnd, PartIndex: 0}); err != nil {
		return model.Response{}, err
	}
	return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("chunk")}}, nil
}

type eventStreamingProvider struct {
	events   []model.StreamEvent
	response model.Response
}

func (p *eventStreamingProvider) Complete(context.Context, model.Request) (model.Response, error) {
	return p.response, nil
}

func (p *eventStreamingProvider) Stream(_ context.Context, _ model.Request, handler model.StreamHandler) (model.Response, error) {
	for _, event := range p.events {
		if err := handler(event); err != nil {
			return model.Response{}, err
		}
		for i := range event.DataDelta {
			event.DataDelta[i] = 0xff
		}
	}
	return p.response, nil
}

type streamInterceptorFunc func(context.Context, model.Request, model.StreamHandler, model.StreamNext) (model.Response, error)

func (f streamInterceptorFunc) InvokeStream(ctx context.Context, request model.Request, handler model.StreamHandler, next model.StreamNext) (model.Response, error) {
	return f(ctx, request, handler, next)
}

func TestRuntimeAppliesDefaultsOrdersInterceptorsAndNormalizesTerminal(t *testing.T) {
	provider := &fakeProvider{}
	events := []string{}
	outer := interceptorFunc(func(ctx context.Context, request model.Request, next pipeline.Next[model.Request, model.Response]) (model.Response, error) {
		events = append(events, "outer-before")
		response, err := next(ctx, request)
		events = append(events, "outer-after")
		return response, err
	})
	inner := interceptorFunc(func(ctx context.Context, request model.Request, next pipeline.Next[model.Request, model.Response]) (model.Response, error) {
		events = append(events, "inner-before")
		response, err := next(ctx, request)
		events = append(events, "inner-after")
		return response, err
	})
	exports, _, err := modelruntime.New(context.Background(), modelruntime.Config{DefaultModel: "m"}, modelruntime.Dependencies{
		Providers: []ingotabi.Named[model.Provider]{{Name: "p", Value: provider}}, Interceptors: []model.Interceptor{outer, inner},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := exports.Runtime.Complete(context.Background(), model.Request{Messages: []model.Message{{Role: model.RoleUser, ToolCalls: nil}}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"outer-before", "inner-before", "inner-after", "outer-after"}
	if len(events) != len(want) {
		t.Fatalf("events=%v", events)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events=%v", events)
		}
	}
	if provider.request.Provider != "p" || provider.request.Model != "m" || response.Provider != "p" || response.Model != "m" {
		t.Fatalf("request=%#v response=%#v", provider.request, response)
	}
}

func TestNewRejectsInvalidDependenciesAndDefaults(t *testing.T) {
	validProvider := &fakeProvider{}
	var typedNilProvider *fakeProvider
	var nilInterceptor interceptorFunc
	var nilStreamInterceptor streamInterceptorFunc

	tests := []struct {
		name string
		cfg  modelruntime.Config
		deps modelruntime.Dependencies
	}{
		{name: "no providers"},
		{name: "duplicate provider names", deps: modelruntime.Dependencies{Providers: []ingotabi.Named[model.Provider]{
			{Name: "p", Value: validProvider}, {Name: "p", Value: validProvider},
		}}},
		{name: "typed nil provider", deps: modelruntime.Dependencies{Providers: []ingotabi.Named[model.Provider]{{Name: "p", Value: typedNilProvider}}}},
		{name: "multiple providers without default", deps: modelruntime.Dependencies{Providers: []ingotabi.Named[model.Provider]{
			{Name: "p1", Value: validProvider}, {Name: "p2", Value: validProvider},
		}}},
		{name: "unknown default provider", cfg: modelruntime.Config{DefaultProvider: "missing"}, deps: modelruntime.Dependencies{
			Providers: []ingotabi.Named[model.Provider]{{Name: "p", Value: validProvider}},
		}},
		{name: "typed nil complete interceptor", deps: modelruntime.Dependencies{
			Providers: []ingotabi.Named[model.Provider]{{Name: "p", Value: validProvider}}, Interceptors: []model.Interceptor{nilInterceptor},
		}},
		{name: "typed nil stream interceptor", deps: modelruntime.Dependencies{
			Providers: []ingotabi.Named[model.Provider]{{Name: "p", Value: validProvider}}, StreamInterceptors: []model.StreamInterceptor{nilStreamInterceptor},
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := modelruntime.New(context.Background(), test.cfg, test.deps)
			if !errors.Is(err, modelruntime.ErrInvalidConfig) {
				t.Fatalf("New() error = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestDefaultsAreAppliedOnlyBeforeInterceptors(t *testing.T) {
	provider := &fakeProvider{}
	tests := []struct {
		name      string
		mutate    func(*model.Request)
		wantError error
	}{
		{name: "provider cleared", mutate: func(request *model.Request) { request.Provider = "" }, wantError: model.ErrProviderNotFound},
		{name: "model cleared", mutate: func(request *model.Request) { request.Model = "" }, wantError: model.ErrModelNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			interceptor := interceptorFunc(func(ctx context.Context, request model.Request, next pipeline.Next[model.Request, model.Response]) (model.Response, error) {
				test.mutate(&request)
				return next(ctx, request)
			})
			exports, _, err := modelruntime.New(context.Background(), modelruntime.Config{DefaultModel: "m"}, modelruntime.Dependencies{
				Providers: []ingotabi.Named[model.Provider]{{Name: "p", Value: provider}}, Interceptors: []model.Interceptor{interceptor},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = exports.Runtime.Complete(context.Background(), model.Request{})
			if !errors.Is(err, test.wantError) {
				t.Fatalf("Complete() error = %v, want %v", err, test.wantError)
			}
		})
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", provider.calls)
	}
}

func TestDefaultModelMayBeSuppliedPerRequest(t *testing.T) {
	provider := &fakeProvider{}
	exports, _, err := modelruntime.New(context.Background(), modelruntime.Config{}, modelruntime.Dependencies{
		Providers: []ingotabi.Named[model.Provider]{{Name: "p", Value: provider}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = exports.Runtime.Complete(context.Background(), model.Request{}); !errors.Is(err, model.ErrModelNotFound) {
		t.Fatalf("missing model error = %v", err)
	}
	if _, err = exports.Runtime.Complete(context.Background(), model.Request{Model: "explicit"}); err != nil {
		t.Fatalf("explicit model: %v", err)
	}
}

func TestResolverMaterializesDefaultsWithoutInvocation(t *testing.T) {
	provider := &fakeProvider{}
	exports, _, err := modelruntime.New(context.Background(), modelruntime.Config{DefaultModel: "default-model"}, modelruntime.Dependencies{
		Providers: []ingotabi.Named[model.Provider]{{Name: "provider", Value: provider}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var _ model.RequestResolver = exports.Resolver

	arguments := json.RawMessage(`{"value":true}`)
	request := model.Request{
		Messages: []model.Message{{Role: model.RoleUser, ToolCalls: []tool.Call{{ID: "call", Name: "tool", Arguments: arguments}}}},
	}
	resolved, err := exports.Resolver.ResolveRequest(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Provider != "provider" || resolved.Model != "default-model" {
		t.Fatalf("resolved selection = (%q, %q)", resolved.Provider, resolved.Model)
	}
	resolved.Messages[0].ToolCalls[0].Arguments[0] = 'X'
	if string(arguments) != `{"value":true}` {
		t.Fatalf("resolver returned aliases to caller input: %s", arguments)
	}
	if provider.calls != 0 {
		t.Fatalf("resolver invoked provider %d times", provider.calls)
	}

	if _, err := exports.Resolver.ResolveRequest(context.Background(), model.Request{Provider: "missing", Model: "m"}); !errors.Is(err, model.ErrProviderNotFound) {
		t.Fatalf("unknown provider error = %v", err)
	}
	withoutDefault, _, err := modelruntime.New(context.Background(), modelruntime.Config{}, modelruntime.Dependencies{
		Providers: []ingotabi.Named[model.Provider]{{Name: "provider", Value: provider}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := withoutDefault.Resolver.ResolveRequest(context.Background(), model.Request{Provider: "provider"}); !errors.Is(err, model.ErrModelNotFound) {
		t.Fatalf("missing model error = %v", err)
	}
}

func TestShortCircuitIsNotSourceNormalizedAndCallerIsOwned(t *testing.T) {
	provider := &fakeProvider{}
	shortArguments := json.RawMessage(`{"cached":true}`)
	short := interceptorFunc(func(context.Context, model.Request, pipeline.Next[model.Request, model.Response]) (model.Response, error) {
		return model.Response{Provider: "cache", Model: "cache", Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("cached"), ToolCalls: []tool.Call{{ID: "cached", Name: "cached", Arguments: shortArguments}}}}, nil
	})
	exports, _, err := modelruntime.New(context.Background(), modelruntime.Config{DefaultModel: "m"}, modelruntime.Dependencies{
		Providers: []ingotabi.Named[model.Provider]{{Name: "p", Value: provider}}, Interceptors: []model.Interceptor{short},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := exports.Runtime.Complete(context.Background(), model.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if text, ok := content.TextOnly(response.Message.Content); provider.calls != 0 || response.Provider != "cache" || response.Model != "cache" || !ok || text != "cached" {
		t.Fatalf("calls=%d response=%#v", provider.calls, response)
	}
	shortArguments[0] = 'X'
	if string(response.Message.ToolCalls[0].Arguments) != `{"cached":true}` {
		t.Fatalf("short-circuit response aliases interceptor data: %s", response.Message.ToolCalls[0].Arguments)
	}

	mutating := &fakeProvider{}
	exports, _, err = modelruntime.New(context.Background(), modelruntime.Config{DefaultModel: "m"}, modelruntime.Dependencies{Providers: []ingotabi.Named[model.Provider]{{Name: "p", Value: mutating}}})
	if err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"x":1}`)
	_, err = exports.Runtime.Complete(context.Background(), model.Request{Messages: []model.Message{{Role: model.RoleUser, ToolCalls: []tool.Call{{ID: "id", Name: "tool", Arguments: original}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if string(original) != `{"x":1}` {
		t.Fatalf("caller arguments=%s", original)
	}
}

func TestRequestClonePreservesPresenceAndOwnership(t *testing.T) {
	temperature := 0.25
	maxTokens := 128
	messageArguments := json.RawMessage(`{"message":true}`)
	toolSchema := json.RawMessage(`{"type":"object"}`)
	stop := []string{"done"}
	inline := []byte("image")
	request := model.Request{
		Messages: []model.Message{
			{Role: model.RoleUser, Content: content.Content{content.Inline(content.KindImage, "image/png", "x.png", inline)}, ToolCalls: make([]tool.Call, 0)},
			{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "call", Name: "tool", Arguments: messageArguments}}},
		},
		Tools:       []tool.Definition{{Name: "tool", InputSchema: toolSchema}},
		Temperature: &temperature,
		MaxTokens:   &maxTokens,
		Stop:        stop,
	}
	provider := providerFunc(func(_ context.Context, received model.Request) (model.Response, error) {
		if received.Messages[0].ToolCalls == nil {
			t.Fatal("non-nil empty message tool calls became nil")
		}
		received.Messages[0].Content[0].Media.Source.Data[0] = 'X'
		received.Messages[1].ToolCalls[0].Arguments[0] = 'X'
		received.Tools[0].InputSchema[0] = 'X'
		received.Stop[0] = "changed"
		*received.Temperature = 1
		*received.MaxTokens = 1
		return model.Response{Message: model.Message{Role: model.RoleAssistant}}, nil
	})
	exports, _, err := modelruntime.New(context.Background(), modelruntime.Config{DefaultModel: "m"}, modelruntime.Dependencies{
		Providers: []ingotabi.Named[model.Provider]{{Name: "p", Value: provider}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = exports.Runtime.Complete(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if string(inline) != "image" || string(messageArguments) != `{"message":true}` || string(toolSchema) != `{"type":"object"}` || stop[0] != "done" || temperature != 0.25 || maxTokens != 128 {
		t.Fatalf("caller request was mutated: arguments=%s schema=%s stop=%v temperature=%v maxTokens=%v", messageArguments, toolSchema, stop, temperature, maxTokens)
	}

	presenceProvider := providerFunc(func(_ context.Context, received model.Request) (model.Response, error) {
		if received.Messages == nil || received.Tools == nil || received.Stop == nil {
			t.Fatalf("non-nil empty aggregate lost presence: %#v", received)
		}
		return model.Response{Message: model.Message{Role: model.RoleAssistant}}, nil
	})
	exports, _, err = modelruntime.New(context.Background(), modelruntime.Config{DefaultModel: "m"}, modelruntime.Dependencies{
		Providers: []ingotabi.Named[model.Provider]{{Name: "p", Value: presenceProvider}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = exports.Runtime.Complete(context.Background(), model.Request{
		Messages: make([]model.Message, 0), Tools: make([]tool.Definition, 0), Stop: make([]string, 0),
	}); err != nil {
		t.Fatal(err)
	}

	emptyRawProvider := providerFunc(func(_ context.Context, received model.Request) (model.Response, error) {
		if received.Messages[0].ToolCalls[0].Arguments == nil || received.Tools[0].InputSchema == nil {
			t.Fatal("non-nil empty RawMessage became nil")
		}
		return model.Response{Message: model.Message{Role: model.RoleAssistant}}, nil
	})
	exports, _, err = modelruntime.New(context.Background(), modelruntime.Config{DefaultModel: "m"}, modelruntime.Dependencies{
		Providers: []ingotabi.Named[model.Provider]{{Name: "p", Value: emptyRawProvider}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = exports.Runtime.Complete(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, ToolCalls: []tool.Call{{Arguments: make(json.RawMessage, 0)}}}},
		Tools:    []tool.Definition{{InputSchema: make(json.RawMessage, 0)}},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestResponseClonePreservesOwnershipAndPresence(t *testing.T) {
	arguments := json.RawMessage(`{"x":1}`)
	inline := []byte("image")
	provider := providerFunc(func(context.Context, model.Request) (model.Response, error) {
		return model.Response{Message: model.Message{
			Role:      model.RoleAssistant,
			Content:   content.Content{content.Inline(content.KindImage, "image/png", "x.png", inline)},
			ToolCalls: []tool.Call{{ID: "call", Name: "tool", Arguments: arguments}},
		}}, nil
	})
	exports, _, err := modelruntime.New(context.Background(), modelruntime.Config{DefaultModel: "m"}, modelruntime.Dependencies{
		Providers: []ingotabi.Named[model.Provider]{{Name: "p", Value: provider}},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := exports.Runtime.Complete(context.Background(), model.Request{})
	if err != nil {
		t.Fatal(err)
	}
	arguments[0] = 'X'
	inline[0] = 'X'
	if string(response.Message.Content[0].Media.Source.Data) != "image" || string(response.Message.ToolCalls[0].Arguments) != `{"x":1}` {
		t.Fatalf("caller response aliases provider data: %s", response.Message.ToolCalls[0].Arguments)
	}

	emptyCallsProvider := providerFunc(func(context.Context, model.Request) (model.Response, error) {
		return model.Response{Message: model.Message{Role: model.RoleAssistant, ToolCalls: make([]tool.Call, 0)}}, nil
	})
	exports, _, err = modelruntime.New(context.Background(), modelruntime.Config{DefaultModel: "m"}, modelruntime.Dependencies{
		Providers: []ingotabi.Named[model.Provider]{{Name: "p", Value: emptyCallsProvider}},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err = exports.Runtime.Complete(context.Background(), model.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if response.Message.ToolCalls == nil {
		t.Fatal("non-nil empty response tool calls became nil")
	}
}

func TestProviderErrorReturnsOwnedPartialResponse(t *testing.T) {
	wantErr := errors.New("provider failed")
	arguments := json.RawMessage(`{"partial":true}`)
	provider := providerFunc(func(context.Context, model.Request) (model.Response, error) {
		return model.Response{Message: model.Message{ToolCalls: []tool.Call{{Arguments: arguments}}}}, wantErr
	})
	exports, _, err := modelruntime.New(context.Background(), modelruntime.Config{DefaultModel: "m"}, modelruntime.Dependencies{
		Providers: []ingotabi.Named[model.Provider]{{Name: "p", Value: provider}},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := exports.Runtime.Complete(context.Background(), model.Request{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Complete() error = %v", err)
	}
	arguments[0] = 'X'
	if string(response.Message.ToolCalls[0].Arguments) != `{"partial":true}` {
		t.Fatalf("partial response aliases provider data: %s", response.Message.ToolCalls[0].Arguments)
	}
}

func TestTerminalRejectsInvalidResponses(t *testing.T) {
	valid := func() model.Response {
		return model.Response{Message: model.Message{Role: model.RoleAssistant}, Usage: model.Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3, Reported: true}}
	}
	tests := []struct {
		name   string
		mutate func(*model.Response)
	}{
		{name: "invalid UTF-8 model", mutate: func(response *model.Response) { response.Model = string([]byte{0xff}) }},
		{name: "invalid UTF-8 content", mutate: func(response *model.Response) {
			response.Message.Content = content.Content{{Kind: content.KindText, Text: string([]byte{0xff})}}
		}},
		{name: "negative usage", mutate: func(response *model.Response) { response.Usage.InputTokens = -1 }},
		{name: "inconsistent usage", mutate: func(response *model.Response) { response.Usage.TotalTokens = 2 }},
		{name: "unreported usage counts", mutate: func(response *model.Response) { response.Usage.Reported = false }},
		{name: "invalid tool call id", mutate: func(response *model.Response) {
			response.Message.ToolCalls = []tool.Call{{ID: string([]byte{0xff}), Name: "tool", Arguments: json.RawMessage(`{}`)}}
		}},
		{name: "invalid tool arguments", mutate: func(response *model.Response) {
			response.Message.ToolCalls = []tool.Call{{ID: "call", Name: "tool", Arguments: json.RawMessage(`{`)}}
		}},
		{name: "invalid UTF-8 tool arguments", mutate: func(response *model.Response) {
			response.Message.ToolCalls = []tool.Call{{ID: "call", Name: "tool", Arguments: json.RawMessage{'"', 0xff, '"'}}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := providerFunc(func(context.Context, model.Request) (model.Response, error) {
				response := valid()
				test.mutate(&response)
				return response, nil
			})
			exports, _, err := modelruntime.New(context.Background(), modelruntime.Config{DefaultModel: "m"}, modelruntime.Dependencies{
				Providers: []ingotabi.Named[model.Provider]{{Name: "p", Value: provider}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = exports.Runtime.Complete(context.Background(), model.Request{}); !errors.Is(err, modelruntime.ErrInvalidResponse) {
				t.Fatalf("Complete() error = %v, want ErrInvalidResponse", err)
			}
		})
	}
}

func TestStreamingUnsupportedAndProviderErrors(t *testing.T) {
	provider := &fakeProvider{}
	exports, _, err := modelruntime.New(context.Background(), modelruntime.Config{DefaultModel: "m"}, modelruntime.Dependencies{Providers: []ingotabi.Named[model.Provider]{{Name: "p", Value: provider}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = exports.Streaming.Stream(context.Background(), model.Request{}, func(model.StreamEvent) error { return nil })
	if !errors.Is(err, model.ErrStreamingUnsupported) {
		t.Fatalf("streaming error=%v", err)
	}
	_, err = exports.Runtime.Complete(context.Background(), model.Request{Provider: "missing", Model: "m"})
	if !errors.Is(err, model.ErrProviderNotFound) {
		t.Fatalf("provider error=%v", err)
	}
}

func TestStreamingChainIsIndependentAndOrdered(t *testing.T) {
	provider := &streamingProvider{}
	events := []string{}
	outer := streamInterceptorFunc(func(ctx context.Context, request model.Request, handler model.StreamHandler, next model.StreamNext) (model.Response, error) {
		events = append(events, "outer-before")
		response, err := next(ctx, request, handler)
		events = append(events, "outer-after")
		return response, err
	})
	inner := streamInterceptorFunc(func(ctx context.Context, request model.Request, handler model.StreamHandler, next model.StreamNext) (model.Response, error) {
		events = append(events, "inner-before")
		response, err := next(ctx, request, handler)
		events = append(events, "inner-after")
		return response, err
	})
	exports, _, err := modelruntime.New(context.Background(), modelruntime.Config{DefaultModel: "m"}, modelruntime.Dependencies{
		Providers: []ingotabi.Named[model.Provider]{{Name: "p", Value: provider}}, StreamInterceptors: []model.StreamInterceptor{outer, inner},
	})
	if err != nil {
		t.Fatal(err)
	}
	var chunks []string
	response, err := exports.Streaming.Stream(context.Background(), model.Request{}, func(event model.StreamEvent) error {
		if event.Kind == model.StreamPartDelta {
			chunks = append(chunks, event.TextDelta)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"outer-before", "inner-before", "inner-after", "outer-after"}
	for i := range want {
		if len(events) != len(want) || events[i] != want[i] {
			t.Fatalf("events=%v", events)
		}
	}
	if len(chunks) != 1 || chunks[0] != "chunk" || response.Provider != "p" || response.Model != "m" {
		t.Fatalf("chunks=%v response=%#v", chunks, response)
	}
}

func TestStreamingValidatesMultipleTextAndMediaParts(t *testing.T) {
	firstData := []byte{1, 2}
	secondData := []byte{3, 4}
	provider := &eventStreamingProvider{
		events: []model.StreamEvent{
			{Kind: model.StreamPartStart, PartIndex: 0, PartKind: content.KindText},
			{Kind: model.StreamPartDelta, PartIndex: 0, TextDelta: "he"},
			{Kind: model.StreamPartDelta, PartIndex: 0, TextDelta: "llo"},
			{Kind: model.StreamPartEnd, PartIndex: 0},
			{Kind: model.StreamPartStart, PartIndex: 1, PartKind: content.KindImage, MIMEType: "image/png", Name: "x.png"},
			{Kind: model.StreamPartDelta, PartIndex: 1, DataDelta: firstData},
			{Kind: model.StreamPartDelta, PartIndex: 1, DataDelta: secondData},
			{Kind: model.StreamPartEnd, PartIndex: 1},
		},
		response: model.Response{Message: model.Message{Role: model.RoleAssistant, Content: content.Content{
			content.Text("hello"), content.Inline(content.KindImage, "image/png", "x.png", []byte{1, 2, 3, 4}),
		}}},
	}
	exports, _, err := modelruntime.New(context.Background(), modelruntime.Config{DefaultModel: "m"}, modelruntime.Dependencies{
		Providers: []ingotabi.Named[model.Provider]{{Name: "p", Value: provider}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var received []model.StreamEvent
	response, err := exports.Streaming.Stream(context.Background(), model.Request{}, func(event model.StreamEvent) error {
		received = append(received, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(received) != len(provider.events) || len(response.Message.Content) != 2 || string(response.Message.Content[1].Media.Source.Data) != string([]byte{1, 2, 3, 4}) {
		t.Fatalf("events=%#v response=%#v", received, response)
	}
	if string(received[5].DataDelta) != string([]byte{1, 2}) || string(received[6].DataDelta) != string([]byte{3, 4}) {
		t.Fatalf("handler data aliases provider buffers: %#v", received)
	}
}

func TestStreamingRejectsInvalidLifecycleAndFinalMismatch(t *testing.T) {
	tests := []struct {
		name    string
		events  []model.StreamEvent
		content content.Content
	}{
		{name: "delta before start", events: []model.StreamEvent{{Kind: model.StreamPartDelta, PartIndex: 0, TextDelta: "x"}}},
		{name: "noncontiguous start", events: []model.StreamEvent{{Kind: model.StreamPartStart, PartIndex: 1, PartKind: content.KindText}}},
		{name: "text data delta", events: []model.StreamEvent{{Kind: model.StreamPartStart, PartIndex: 0, PartKind: content.KindText}, {Kind: model.StreamPartDelta, PartIndex: 0, DataDelta: []byte{1}}}},
		{name: "media text delta", events: []model.StreamEvent{{Kind: model.StreamPartStart, PartIndex: 0, PartKind: content.KindImage}, {Kind: model.StreamPartDelta, PartIndex: 0, TextDelta: "x"}}},
		{name: "incomplete", events: []model.StreamEvent{{Kind: model.StreamPartStart, PartIndex: 0, PartKind: content.KindText}}},
		{name: "final mismatch", events: []model.StreamEvent{{Kind: model.StreamPartStart, PartIndex: 0, PartKind: content.KindText}, {Kind: model.StreamPartDelta, PartIndex: 0, TextDelta: "x"}, {Kind: model.StreamPartEnd, PartIndex: 0}}, content: content.FromText("y")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &eventStreamingProvider{events: test.events, response: model.Response{Message: model.Message{Role: model.RoleAssistant, Content: test.content}}}
			exports, _, err := modelruntime.New(context.Background(), modelruntime.Config{DefaultModel: "m"}, modelruntime.Dependencies{
				Providers: []ingotabi.Named[model.Provider]{{Name: "p", Value: provider}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := exports.Streaming.Stream(context.Background(), model.Request{}, func(model.StreamEvent) error { return nil }); !errors.Is(err, modelruntime.ErrInvalidResponse) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestStreamPropagatesHandlerError(t *testing.T) {
	provider := &streamingProvider{}
	exports, _, err := modelruntime.New(context.Background(), modelruntime.Config{DefaultModel: "m"}, modelruntime.Dependencies{
		Providers: []ingotabi.Named[model.Provider]{{Name: "p", Value: provider}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("stop chunks")
	_, err = exports.Streaming.Stream(context.Background(), model.Request{}, func(model.StreamEvent) error { return wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("Stream() error = %v, want handler error", err)
	}
}

func TestConcurrentProviderSelection(t *testing.T) {
	first := &countingProvider{}
	second := &countingProvider{}
	exports, _, err := modelruntime.New(context.Background(), modelruntime.Config{DefaultProvider: "first"}, modelruntime.Dependencies{
		Providers: []ingotabi.Named[model.Provider]{{Name: "first", Value: first}, {Name: "second", Value: second}},
	})
	if err != nil {
		t.Fatal(err)
	}

	const callsPerProvider = 25
	var wait sync.WaitGroup
	errorsFound := make(chan error, callsPerProvider*2)
	for i := 0; i < callsPerProvider*2; i++ {
		provider := "first"
		if i%2 != 0 {
			provider = "second"
		}
		wait.Add(1)
		go func() {
			defer wait.Done()
			response, callErr := exports.Runtime.Complete(context.Background(), model.Request{Provider: provider, Model: "m"})
			if callErr != nil {
				errorsFound <- callErr
				return
			}
			if response.Provider != provider || response.Model != "m" {
				errorsFound <- errors.New("response source was not normalized")
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for callErr := range errorsFound {
		t.Error(callErr)
	}
	if first.calls.Load() != callsPerProvider || second.calls.Load() != callsPerProvider {
		t.Fatalf("provider calls = (%d, %d), want (%d, %d)", first.calls.Load(), second.calls.Load(), callsPerProvider, callsPerProvider)
	}
}

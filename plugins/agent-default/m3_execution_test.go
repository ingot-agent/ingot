package agentdefault

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/prompt"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

type completeModelFunc func(context.Context, model.Request) (model.Response, error)

func (f completeModelFunc) Complete(ctx context.Context, request model.Request) (model.Response, error) {
	return f(ctx, request)
}

type toolRuntimeFunc struct {
	calls []tool.Call
	call  func(context.Context, tool.Call) (tool.Result, error)
}

type commitThenFailStore struct {
	*memoryStore
	failAt  int
	appends int
	err     error
}

func (s *commitThenFailStore) Append(ctx context.Context, id session.ID, entry session.Entry) error {
	s.appends++
	if err := s.memoryStore.Append(ctx, id, entry); err != nil {
		return err
	}
	if s.appends == s.failAt {
		return s.err
	}
	return nil
}

func (r *toolRuntimeFunc) Definitions() []tool.Definition {
	return []tool.Definition{
		{Name: "a", Description: "a", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "b", Description: "b", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "c", Description: "c", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
}

func (r *toolRuntimeFunc) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	r.calls = append(r.calls, cloneCall(call))
	return r.call(ctx, call)
}

func TestStreamFallbackRequiresZeroDeliveredAgentEvents(t *testing.T) {
	providerErr := errors.New("provider failed")
	consumerErr := errors.New("consumer stopped")
	for _, tc := range []struct {
		name         string
		stream       func(model.StreamHandler) error
		handler      agent.StreamHandler
		wantErr      error
		wantComplete int
		wantEvents   int
	}{
		{
			name: "unsupported before events falls back",
			stream: func(model.StreamHandler) error {
				return model.ErrStreamingUnsupported
			},
			wantComplete: 1,
		},
		{
			name: "unmapped model progress does not block fallback",
			stream: func(handler model.StreamHandler) error {
				if err := handler(model.StreamEvent{Kind: model.StreamPartStart, PartKind: content.KindText}); err != nil {
					return err
				}
				return model.ErrStreamingUnsupported
			},
			wantComplete: 1,
		},
		{
			name: "delivered event blocks fallback",
			stream: func(handler model.StreamHandler) error {
				if err := handler(model.StreamEvent{Kind: model.StreamPartDelta, TextDelta: "partial"}); err != nil {
					return err
				}
				return model.ErrStreamingUnsupported
			},
			wantErr:    model.ErrStreamingUnsupported,
			wantEvents: 1,
		},
		{
			name: "first handler error blocks fallback",
			stream: func(handler model.StreamHandler) error {
				_ = handler(model.StreamEvent{Kind: model.StreamPartDelta, TextDelta: "partial"})
				return model.ErrStreamingUnsupported
			},
			handler:    func(agent.StreamEvent) error { return consumerErr },
			wantErr:    consumerErr,
			wantEvents: 1,
		},
		{
			name: "ordinary stream error never falls back",
			stream: func(model.StreamHandler) error {
				return providerErr
			},
			wantErr: providerErr,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
			completeCalls := 0
			var completeRequest model.Request
			complete := completeModelFunc(func(_ context.Context, request model.Request) (model.Response, error) {
				completeCalls++
				completeRequest = cloneModelRequest(request)
				return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("done")}}, nil
			})
			var streamRequest model.Request
			streaming := modelStreamFunc(func(_ context.Context, request model.Request, handler model.StreamHandler) (model.Response, error) {
				streamRequest = cloneModelRequest(request)
				return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("ignore")}}, tc.stream(handler)
			})
			exports, _, err := New(context.Background(), Config{}, Dependencies{
				Model: complete, Streaming: ingotabi.Some[model.StreamingRuntime](streaming), Tools: &fakeTools{},
				Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{},
			})
			if err != nil {
				t.Fatal(err)
			}
			events := 0
			handler := tc.handler
			if handler == nil {
				handler = func(agent.StreamEvent) error { events++; return nil }
			} else {
				configured := handler
				handler = func(event agent.StreamEvent) error { events++; return configured(event) }
			}
			result, err := exports.Streaming.Stream(context.Background(), agent.Turn{SessionID: "s", Input: "hello"}, handler)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error=%v want=%v", err, tc.wantErr)
			}
			if tc.wantErr == nil && textValue(result.Output) != "done" {
				t.Fatalf("result=%#v", result)
			}
			if tc.wantErr != nil && len(result.Output) != 0 {
				t.Fatalf("error returned non-zero canonical result: %#v", result)
			}
			if completeCalls != tc.wantComplete || events != tc.wantEvents {
				t.Fatalf("complete=%d events=%d", completeCalls, events)
			}
			if tc.wantComplete == 1 && !reflect.DeepEqual(streamRequest, completeRequest) {
				t.Fatalf("fallback request changed:\nstream=%#v\ncomplete=%#v", streamRequest, completeRequest)
			}
			wantEntries := 1
			if tc.wantErr == nil {
				wantEntries = 2
			}
			if len(store.entries["s"]) != wantEntries {
				t.Fatalf("entries=%d want=%d", len(store.entries["s"]), wantEntries)
			}
		})
	}
}

func TestStreamingFallbackObservesBothRealModelAttempts(t *testing.T) {
	consumer := &recordingConsumer{}
	complete := completeModelFunc(func(context.Context, model.Request) (model.Response, error) {
		return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("done")}}, nil
	})
	streaming := modelStreamFunc(func(context.Context, model.Request, model.StreamHandler) (model.Response, error) {
		return model.Response{}, model.ErrStreamingUnsupported
	})
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: complete, Streaming: ingotabi.Some[model.StreamingRuntime](streaming), Tools: &fakeTools{},
		Store: &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}, Assets: newMemoryAssets(),
		Prompt: passthroughPrompt{}, Observation: consumer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exports.Streaming.Stream(context.Background(), agent.Turn{SessionID: "s", Input: "hello"}, func(agent.StreamEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	var started int
	var statuses []observation.Status
	for _, event := range consumer.snapshot() {
		switch detail := event.Detail.(type) {
		case observation.ModelStarted:
			started++
		case observation.ModelFinished:
			statuses = append(statuses, detail.Status)
		}
	}
	if started != 2 || !reflect.DeepEqual(statuses, []observation.Status{observation.StatusFailed, observation.StatusSucceeded}) {
		t.Fatalf("model starts=%d statuses=%v", started, statuses)
	}
}

func TestPreDispatchToolRejectionsContinueButOtherErrorsStop(t *testing.T) {
	postDispatchErr := errors.New("transport failed after dispatch")
	for _, tc := range []struct {
		name           string
		failure        error
		wantErr        error
		wantToolCalls  []string
		wantEntries    int
		wantModelCalls int
		wantSynthetic  string
	}{
		{"not found", tool.ErrNotFound, nil, []string{"a", "b", "c"}, 6, 2, "tool error [not_found]: requested tool is unavailable"},
		{"invalid arguments", tool.ErrInvalidArguments, nil, []string{"a", "b", "c"}, 6, 2, "tool error [invalid_arguments]: tool arguments were rejected"},
		{"post-dispatch error", postDispatchErr, postDispatchErr, []string{"a", "b"}, 3, 1, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
			models := &sequenceModel{responses: []model.Response{
				{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{
					{ID: "call-a", Name: "a", Arguments: json.RawMessage(`{}`)},
					{ID: "call-b", Name: "b", Arguments: json.RawMessage(`{}`)},
					{ID: "call-c", Name: "c", Arguments: json.RawMessage(`{}`)},
				}}},
				{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("done")}},
			}}
			tools := &toolRuntimeFunc{call: func(_ context.Context, call tool.Call) (tool.Result, error) {
				if call.Name == "b" {
					return tool.Result{Content: content.FromText("must be ignored")}, tc.failure
				}
				return tool.Result{Content: content.FromText("ok-" + call.Name)}, nil
			}}
			exports, _, err := New(context.Background(), Config{}, Dependencies{
				Model: models, Tools: tools, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{},
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error=%v want=%v", err, tc.wantErr)
			}
			if tc.wantErr != nil && len(result.Output) != 0 {
				t.Fatalf("error returned non-zero canonical result: %#v", result)
			}
			var names []string
			for _, call := range tools.calls {
				names = append(names, call.Name)
			}
			if !reflect.DeepEqual(names, tc.wantToolCalls) || len(store.entries["s"]) != tc.wantEntries || len(models.requests) != tc.wantModelCalls {
				t.Fatalf("calls=%v entries=%d models=%d", names, len(store.entries["s"]), len(models.requests))
			}
			if tc.wantSynthetic != "" {
				message, decodeErr := decodePersistedMessage(store.entries["s"][3].Payload)
				if decodeErr != nil || message.ToolCallID != "call-b" || textValue(message.Content) != tc.wantSynthetic {
					t.Fatalf("synthetic=%#v error=%v", message, decodeErr)
				}
			}
		})
	}
}

type cancelingPrompt struct{ cancel context.CancelFunc }

func (p cancelingPrompt) Render(_ context.Context, request prompt.Request) ([]model.Message, error) {
	p.cancel()
	return append(cloneMessages(request.History), model.Message{Role: model.RoleUser, Content: request.Input}), nil
}

func TestCancellationAfterDurableUserStopsBeforeModelDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	modelCalls := 0
	complete := completeModelFunc(func(context.Context, model.Request) (model.Response, error) {
		modelCalls++
		return model.Response{Message: model.Message{Role: model.RoleAssistant}}, nil
	})
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: complete, Tools: &fakeTools{}, Store: store, Assets: newMemoryAssets(), Prompt: cancelingPrompt{cancel: cancel},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := exports.Runtime.Run(ctx, agent.Turn{SessionID: "s", Input: "hello"})
	if !errors.Is(err, context.Canceled) || len(result.Output) != 0 || modelCalls != 0 || len(store.entries["s"]) != 1 {
		t.Fatalf("result=%#v error=%v models=%d entries=%d", result, err, modelCalls, len(store.entries["s"]))
	}
}

func TestCancellationAfterModelSuccessStopsBeforeAssistantPersistence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	consumer := &recordingConsumer{}
	complete := completeModelFunc(func(context.Context, model.Request) (model.Response, error) {
		cancel()
		return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("known but not durable")}}, nil
	})
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: complete, Tools: &fakeTools{}, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{}, Observation: consumer,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := exports.Runtime.Run(ctx, agent.Turn{SessionID: "s", Input: "hello"})
	if !errors.Is(err, context.Canceled) || len(result.Output) != 0 || len(store.entries["s"]) != 1 {
		t.Fatalf("result=%#v error=%v entries=%d", result, err, len(store.entries["s"]))
	}
	modelSucceeded := false
	for _, event := range consumer.snapshot() {
		if detail, ok := event.Detail.(observation.ModelFinished); ok {
			modelSucceeded = detail.Status == observation.StatusSucceeded && detail.Response != nil
		}
	}
	if !modelSucceeded {
		t.Fatal("authoritative model completion was not observed as succeeded")
	}
}

func TestCancellationAfterToolSuccessDoesNotPersistVolatileResultOrDispatchNext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	consumer := &recordingConsumer{}
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	models := &sequenceModel{responses: []model.Response{{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{
		{ID: "call-a", Name: "a", Arguments: json.RawMessage(`{}`)},
		{ID: "call-b", Name: "b", Arguments: json.RawMessage(`{}`)},
	}}}}}
	tools := &toolRuntimeFunc{call: func(_ context.Context, call tool.Call) (tool.Result, error) {
		if call.Name == "a" {
			cancel()
		}
		return tool.Result{Content: content.FromText("known but not durable")}, nil
	}}
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: models, Tools: tools, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{}, Observation: consumer,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := exports.Runtime.Run(ctx, agent.Turn{SessionID: "s", Input: "hello"})
	if !errors.Is(err, context.Canceled) || len(result.Output) != 0 || len(tools.calls) != 1 || len(store.entries["s"]) != 2 {
		t.Fatalf("result=%#v error=%v tools=%d entries=%d", result, err, len(tools.calls), len(store.entries["s"]))
	}
	toolSucceeded := false
	turnCanceled := false
	for _, event := range consumer.snapshot() {
		switch detail := event.Detail.(type) {
		case observation.ToolFinished:
			toolSucceeded = detail.Status == observation.StatusSucceeded && detail.Result != nil
		case observation.TurnFinished:
			turnCanceled = detail.Status == observation.StatusCanceled && detail.Result == nil
		}
	}
	if !toolSucceeded || !turnCanceled {
		t.Fatalf("toolSucceeded=%v turnCanceled=%v", toolSucceeded, turnCanceled)
	}
}

func TestPersistenceErrorsStopAllDependentWork(t *testing.T) {
	persistErr := errors.New("append outcome unknown")
	toolDecision := model.Response{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{
		{ID: "call-a", Name: "a", Arguments: json.RawMessage(`{}`)},
		{ID: "call-b", Name: "b", Arguments: json.RawMessage(`{}`)},
	}}}
	for _, tc := range []struct {
		name        string
		failAt      int
		response    model.Response
		wantEntries int
		wantModels  int
		wantTools   int
	}{
		{"user append", 1, toolDecision, 0, 0, 0},
		{"assistant append", 2, toolDecision, 1, 1, 0},
		{"tool result append", 3, toolDecision, 2, 1, 1},
		{"final assistant append", 2, model.Response{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("done")}}, 1, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &failingAppendStore{
				memoryStore: &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}},
				failAt:      tc.failAt,
				err:         persistErr,
			}
			models := &sequenceModel{responses: []model.Response{tc.response}}
			tools := &toolRuntimeFunc{call: func(context.Context, tool.Call) (tool.Result, error) {
				return tool.Result{Content: content.FromText("ok")}, nil
			}}
			exports, _, err := New(context.Background(), Config{}, Dependencies{
				Model: models, Tools: tools, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{},
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
			if !errors.Is(err, persistErr) || len(result.Output) != 0 || len(store.entries["s"]) != tc.wantEntries ||
				len(models.requests) != tc.wantModels || len(tools.calls) != tc.wantTools || store.appends != tc.failAt {
				t.Fatalf("result=%#v error=%v entries=%d models=%d tools=%d appends=%d", result, err,
					len(store.entries["s"]), len(models.requests), len(tools.calls), store.appends)
			}
		})
	}
}

func TestRecoveryPersistenceErrorStopsBeforeNewTurnWork(t *testing.T) {
	persistErr := errors.New("recovery append outcome unknown")
	assistant := model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{
		{ID: "call-a", Name: "a", Arguments: json.RawMessage(`{}`)},
		{ID: "call-b", Name: "b", Arguments: json.RawMessage(`{}`)},
	}}
	payload, err := encodePersistedMessage(assistant)
	if err != nil {
		t.Fatal(err)
	}
	store := &failingAppendStore{
		memoryStore: &memoryStore{entries: map[session.ID][]session.Entry{"s": {{
			Kind: agentMessageKind, Version: agentMessageVersion, Payload: payload,
		}}}},
		failAt: 1,
		err:    persistErr,
	}
	models := &sequenceModel{responses: []model.Response{{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("must not run")}}}}
	tools := &toolRuntimeFunc{call: func(context.Context, tool.Call) (tool.Result, error) {
		return tool.Result{Content: content.FromText("must not run")}, nil
	}}
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: models, Tools: tools, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "new input"})
	if !errors.Is(err, persistErr) || len(result.Output) != 0 || store.appends != 1 ||
		len(store.entries["s"]) != 1 || len(models.requests) != 0 || len(tools.calls) != 0 {
		t.Fatalf("result=%#v error=%v appends=%d entries=%d models=%d tools=%d", result, err,
			store.appends, len(store.entries["s"]), len(models.requests), len(tools.calls))
	}
}

func TestRecoveryUsesCommittedHistoryAfterAppendReturnedError(t *testing.T) {
	persistErr := errors.New("assistant append status unknown")
	store := &commitThenFailStore{
		memoryStore: &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}},
		failAt:      2,
		err:         persistErr,
	}
	models := &sequenceModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "call-a", Name: "echo", Arguments: json.RawMessage(`{}`)}}}},
		{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("continued safely")}},
	}}
	tools := &fakeTools{}
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: models, Tools: tools, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "first"})
	if !errors.Is(err, persistErr) || len(result.Output) != 0 || len(store.entries["s"]) != 2 || len(tools.calls) != 0 {
		t.Fatalf("first result=%#v error=%v entries=%d tools=%d", result, err, len(store.entries["s"]), len(tools.calls))
	}
	result, err = exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "next"})
	if err != nil || textValue(result.Output) != "continued safely" || len(store.entries["s"]) != 5 || len(tools.calls) != 0 {
		t.Fatalf("second result=%#v error=%v entries=%d tools=%d", result, err, len(store.entries["s"]), len(tools.calls))
	}
	recovered, err := decodePersistedMessage(store.entries["s"][2].Payload)
	if err != nil || recovered.ToolCallID != "call-a" || textValue(recovered.Content) != interruptedContent {
		t.Fatalf("recovered=%#v error=%v", recovered, err)
	}
}

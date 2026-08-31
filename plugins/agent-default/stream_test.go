package agentdefault

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/pipeline"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

type modelStreamFunc func(context.Context, model.Request, model.StreamHandler) (model.Response, error)

func (f modelStreamFunc) Stream(ctx context.Context, request model.Request, handler model.StreamHandler) (model.Response, error) {
	return f(ctx, request, handler)
}

func TestModelEventMapping(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event model.StreamEvent
		kind  agent.StreamEventKind
	}{
		{"reasoning", model.StreamEvent{Kind: model.StreamPartDelta, Semantic: model.StreamSemanticReasoning, TextDelta: "x"}, agent.StreamReasoningDelta},
		{"legacy output", model.StreamEvent{Kind: model.StreamPartDelta, TextDelta: "x"}, agent.StreamOutputDelta},
		{"explicit text", model.StreamEvent{Kind: model.StreamPartDelta, PartKind: content.KindText, TextDelta: "x"}, agent.StreamOutputDelta},
		{"start", model.StreamEvent{Kind: model.StreamPartStart, PartKind: content.KindText}, 0},
		{"end", model.StreamEvent{Kind: model.StreamPartEnd}, 0},
		{"empty", model.StreamEvent{Kind: model.StreamPartDelta}, 0},
		{"binary", model.StreamEvent{Kind: model.StreamPartDelta, DataDelta: []byte("x")}, 0},
		{"nontext", model.StreamEvent{Kind: model.StreamPartDelta, PartKind: content.KindAudio, TextDelta: "x"}, 0},
		{"future tool event", model.StreamEvent{Kind: 255, TextDelta: "tool arguments"}, 0},
		{"unknown semantic", model.StreamEvent{Kind: model.StreamPartDelta, Semantic: 255, TextDelta: "x"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event, ok := mapModelStreamEvent(tc.event)
			if ok != (tc.kind != 0) || event.Kind != tc.kind || (ok && event.TextDelta != tc.event.TextDelta) {
				t.Fatalf("event=%v ok=%v", event, ok)
			}
		})
	}
}

func TestRunStreamEquivalentAcrossToolRounds(t *testing.T) {
	type observation struct {
		result      agent.Result
		entries     []session.Entry
		history     []model.Message
		requests    []model.Request
		compactions []contextwindow.CompactionRequest
		calls       []tool.Call
		trace       []string
	}
	execute := func(stream bool, legacyConfig bool) observation {
		store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
		models := &sequenceModel{responses: []model.Response{
			{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("working"), ToolCalls: []tool.Call{{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{}`)}}}},
			{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "c2", Name: "echo", Arguments: json.RawMessage(`{}`)}}}},
			{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("done")}},
		}}
		tools := &fakeTools{}
		compactor := &recordingCompactor{}
		var trace []string
		interceptor := agentInterceptorFunc(func(ctx context.Context, turn agent.Turn, next pipeline.Next[agent.Turn, agent.Result]) (agent.Result, error) {
			trace = append(trace, "before")
			result, err := next(ctx, turn)
			trace = append(trace, "after")
			return result, err
		})
		streamCalls := 0
		streaming := modelStreamFunc(func(ctx context.Context, req model.Request, handler model.StreamHandler) (model.Response, error) {
			streamCalls++
			response, err := models.Complete(ctx, req)
			if err != nil {
				return response, err
			}
			for _, text := range []string{"think", " more"} {
				if err := handler(model.StreamEvent{Kind: model.StreamPartDelta, Semantic: model.StreamSemanticReasoning, TextDelta: text}); err != nil {
					return model.Response{}, err
				}
			}
			for _, part := range response.Message.Content {
				if err := handler(model.StreamEvent{Kind: model.StreamPartDelta, TextDelta: part.Text}); err != nil {
					return model.Response{}, err
				}
			}
			return response, nil
		})
		exports, cleanup, err := New(context.Background(), Config{Streaming: legacyConfig}, Dependencies{
			Model: models, Streaming: ingotabi.Some[model.StreamingRuntime](streaming), Tools: tools,
			Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{},
			Compactor: ingotabi.Some[contextwindow.Compactor](compactor), Interceptors: []agent.Interceptor{interceptor},
		})
		if err != nil || cleanup != nil || exports.Runtime != exports.Streaming.(agent.Runtime) {
			t.Fatalf("exports=%v cleanup=%v err=%v", exports, cleanup != nil, err)
		}
		turn := agent.Turn{SessionID: "s", Input: "hello"}
		var result agent.Result
		var events []agent.StreamEvent
		if stream {
			result, err = exports.Streaming.Stream(context.Background(), turn, func(event agent.StreamEvent) error { events = append(events, event); return nil })
		} else {
			result, err = exports.Runtime.Run(context.Background(), turn)
		}
		if err != nil {
			t.Fatal(err)
		}
		if stream {
			want := []agent.StreamEvent{
				{Kind: agent.StreamReasoningDelta, TextDelta: "think"}, {Kind: agent.StreamReasoningDelta, TextDelta: " more"}, {Kind: agent.StreamOutputDelta, TextDelta: "working"},
				{Kind: agent.StreamReasoningDelta, TextDelta: "think"}, {Kind: agent.StreamReasoningDelta, TextDelta: " more"},
				{Kind: agent.StreamReasoningDelta, TextDelta: "think"}, {Kind: agent.StreamReasoningDelta, TextDelta: " more"}, {Kind: agent.StreamOutputDelta, TextDelta: "done"},
			}
			if !reflect.DeepEqual(events, want) || streamCalls != 3 {
				t.Fatalf("events=%v streamCalls=%d", events, streamCalls)
			}
		} else if streamCalls != 0 {
			t.Fatal("Run invoked Stream")
		}
		entries, err := store.Load(context.Background(), "s")
		if err != nil {
			t.Fatal(err)
		}
		history, err := exports.History.Load(context.Background(), "s")
		if err != nil {
			t.Fatal(err)
		}
		return observation{result, entries, history, models.requests, compactor.requests, tools.calls, trace}
	}
	want := execute(false, false)
	for _, legacyConfig := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			got := execute(stream, legacyConfig)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("stream=%v legacyConfig=%v\ngot=%#v\nwant=%#v", stream, legacyConfig, got, want)
			}
		}
	}
}

func TestStreamingErrorsStopTurnAndReleaseSession(t *testing.T) {
	for _, mode := range []string{"handler", "canceled", "unsupported", "model error"} {
		t.Run(mode, func(t *testing.T) {
			store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
			tools := &fakeTools{}
			models := &sequenceModel{responses: []model.Response{{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("recovered")}}}}
			consumerErr := errors.New("consumer closed")
			providerErr := errors.New("provider failed")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			attempts := 0
			streaming := modelStreamFunc(func(callCtx context.Context, _ model.Request, handler model.StreamHandler) (model.Response, error) {
				attempts++
				if mode == "unsupported" {
					return model.Response{}, model.ErrStreamingUnsupported
				}
				if mode == "model error" {
					return model.Response{}, providerErr
				}
				if callCtx != ctx {
					t.Fatal("context was replaced")
				}
				// Deliberately ignore callback errors: the agent must still abort.
				_ = handler(model.StreamEvent{Kind: model.StreamPartDelta, TextDelta: "stop"})
				_ = handler(model.StreamEvent{Kind: model.StreamPartDelta, TextDelta: "too late"})
				return model.Response{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "c", Name: "echo", Arguments: json.RawMessage(`{}`)}}}}, nil
			})
			exports, _, err := New(context.Background(), Config{}, Dependencies{Model: models, Streaming: ingotabi.Some[model.StreamingRuntime](streaming), Tools: tools, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{}})
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			_, err = exports.Streaming.Stream(ctx, agent.Turn{SessionID: "s", Input: "hello"}, func(agent.StreamEvent) error {
				count++
				if mode == "canceled" {
					cancel()
					return nil
				}
				return consumerErr
			})
			wantErr := map[string]error{"handler": consumerErr, "canceled": context.Canceled, "unsupported": agent.ErrStreamingUnsupported, "model error": providerErr}[mode]
			if !errors.Is(err, wantErr) || attempts != 1 || len(tools.calls) != 0 || len(models.requests) != 0 {
				t.Fatalf("err=%v attempts=%d tools=%v", err, attempts, tools.calls)
			}
			if mode == "handler" && (err != consumerErr || count != 1) {
				t.Fatalf("handler error identity/count: %v/%d", err, count)
			}
			if mode == "unsupported" && !errors.Is(err, model.ErrStreamingUnsupported) {
				t.Fatal("lost model error")
			}
			entries, _ := store.Load(context.Background(), "s")
			if len(entries) != 1 {
				t.Fatalf("partial assistant persisted: %v", entries)
			}
			resumeCtx, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			if _, err := exports.Runtime.Run(resumeCtx, agent.Turn{SessionID: "s", Input: "retry"}); err != nil {
				t.Fatalf("session gate not released: %v", err)
			}
		})
	}
}

func TestStreamRejectsMissingCapabilityAndNilHandlerBeforeSideEffects(t *testing.T) {
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	models := &sequenceModel{responses: []model.Response{{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("ok")}}}}
	exports, _, err := New(context.Background(), Config{Streaming: true}, Dependencies{Model: models, Tools: &fakeTools{}, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{}})
	if err != nil {
		t.Fatal(err)
	}
	turn := agent.Turn{SessionID: "s", Input: "hello"}
	if _, err := exports.Streaming.Stream(context.Background(), turn, nil); err != agent.ErrNilStreamHandler {
		t.Fatalf("nil handler: %v", err)
	}
	if _, err := exports.Streaming.Stream(context.Background(), turn, func(agent.StreamEvent) error { return nil }); err != agent.ErrStreamingUnsupported {
		t.Fatalf("unsupported: %v", err)
	}
	if len(store.entries["s"]) != 0 || len(models.requests) != 0 {
		t.Fatal("failed stream had side effects")
	}
	if _, err := exports.Runtime.Run(context.Background(), turn); err != nil {
		t.Fatal(err)
	}
}

func TestStreamSharesSessionGateWithRunAndHistory(t *testing.T) {
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}, "other": {}}}
	models := &sequenceModel{responses: []model.Response{{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("other session")}}}}
	started := make(chan struct{})
	streaming := modelStreamFunc(func(ctx context.Context, _ model.Request, handler model.StreamHandler) (model.Response, error) {
		if err := handler(model.StreamEvent{Kind: model.StreamPartDelta, TextDelta: "working"}); err != nil {
			return model.Response{}, err
		}
		close(started)
		<-ctx.Done()
		return model.Response{}, ctx.Err()
	})
	exports, _, err := New(context.Background(), Config{}, Dependencies{Model: models, Streaming: ingotabi.Some[model.StreamingRuntime](streaming), Tools: &fakeTools{}, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := exports.Streaming.Stream(ctx, agent.Turn{SessionID: "s", Input: "hello"}, func(agent.StreamEvent) error { return nil })
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not start")
	}
	for _, operation := range []string{"Run", "History"} {
		waiting, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
		if operation == "Run" {
			_, err = exports.Runtime.Run(waiting, agent.Turn{SessionID: "s", Input: "wait"})
		} else {
			_, err = exports.History.Load(waiting, "s")
		}
		stop()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s did not wait for the stream: %v", operation, err)
		}
	}
	otherCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if _, err := exports.Runtime.Run(otherCtx, agent.Turn{SessionID: "other", Input: "hello"}); err != nil {
		t.Fatalf("other session blocked: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not return after cancellation")
	}
	if _, err := exports.History.Load(otherCtx, "s"); err != nil {
		t.Fatalf("gate not released: %v", err)
	}
}

func TestAgentInterceptorCannotSwallowConsumerFailure(t *testing.T) {
	consumerErr := errors.New("consumer closed")
	streamCalls := 0
	streaming := modelStreamFunc(func(_ context.Context, _ model.Request, handler model.StreamHandler) (model.Response, error) {
		streamCalls++
		return model.Response{}, handler(model.StreamEvent{Kind: model.StreamPartDelta, TextDelta: "stop"})
	})
	interceptor := agentInterceptorFunc(func(ctx context.Context, turn agent.Turn, next pipeline.Next[agent.Turn, agent.Result]) (agent.Result, error) {
		_, _ = next(ctx, turn)
		_, _ = next(ctx, turn)
		return agent.Result{Output: content.FromText("masked error")}, nil
	})
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: &sequenceModel{}, Streaming: ingotabi.Some[model.StreamingRuntime](streaming), Tools: &fakeTools{},
		Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{}, Interceptors: []agent.Interceptor{interceptor},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = exports.Streaming.Stream(context.Background(), agent.Turn{SessionID: "s", Input: "hello"}, func(agent.StreamEvent) error { return consumerErr })
	if err != consumerErr || streamCalls != 1 || len(store.entries["s"]) != 1 {
		t.Fatalf("error=%v calls=%d entries=%d", err, streamCalls, len(store.entries["s"]))
	}
}

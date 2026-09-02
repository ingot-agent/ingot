package agentdefault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/pipeline"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

type recordingConsumer struct {
	mu     sync.Mutex
	events []observation.Event
	seq    map[observation.ID]uint64
}

func (r *recordingConsumer) Emit(ctx context.Context, detail observation.Detail) {
	correlation, _ := observation.CorrelationFromContext(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seq == nil {
		r.seq = make(map[observation.ID]uint64)
	}
	r.seq[correlation.TurnID]++
	r.events = append(r.events, observation.Event{Sequence: r.seq[correlation.TurnID], Correlation: correlation, Detail: detail})
}

func (r *recordingConsumer) snapshot() []observation.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]observation.Event(nil), r.events...)
}

type progressTools struct {
	observation observation.Consumer
	err         error
}

type failingAppendStore struct {
	*memoryStore
	failAt  int
	appends int
	err     error
}

func (s *failingAppendStore) Append(ctx context.Context, id session.ID, entry session.Entry) error {
	s.appends++
	if s.appends == s.failAt {
		return s.err
	}
	return s.memoryStore.Append(ctx, id, entry)
}

type panicModel struct{}

func (panicModel) Complete(context.Context, model.Request) (model.Response, error) {
	panic("model panic")
}

func (t *progressTools) Definitions() []tool.Definition {
	return []tool.Definition{{Name: "echo", Description: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}}
}

func (t *progressTools) Call(ctx context.Context, _ tool.Call) (tool.Result, error) {
	t.observation.Emit(ctx, observation.ToolProgress{Progress: tool.Progress{Channel: "stdout", Content: content.FromText("working")}})
	if t.err != nil {
		return tool.Result{}, t.err
	}
	return tool.Result{Content: content.FromText("tool-ok")}, nil
}

func TestExecutionObservationLifecycleProgressCorrelationAndSequence(t *testing.T) {
	consumer := &recordingConsumer{}
	models := &sequenceModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "call-1", Name: "echo", Arguments: json.RawMessage(`{}`)}}}},
		{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("done")}},
	}}
	streaming := modelStreamFunc(func(ctx context.Context, request model.Request, handler model.StreamHandler) (model.Response, error) {
		response, err := models.Complete(ctx, request)
		if err != nil {
			return model.Response{}, err
		}
		if err := handler(model.StreamEvent{Kind: model.StreamPartDelta, TextDelta: "delta", DataDelta: []byte{1, 2}}); err != nil {
			return model.Response{}, err
		}
		return response, nil
	})
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: models, Streaming: ingotabi.Some[model.StreamingRuntime](streaming),
		Tools: &progressTools{observation: consumer}, Store: &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}},
		Assets: newMemoryAssets(), Prompt: passthroughPrompt{}, Observation: consumer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exports.Streaming.Stream(context.Background(), agent.Turn{SessionID: "s", Input: "hello"}, func(agent.StreamEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	events := consumer.snapshot()
	wantTypes := []any{
		observation.TurnStarted{},
		observation.RoundStarted{}, observation.ModelStarted{}, observation.ModelProgress{}, observation.ModelFinished{},
		observation.ToolStarted{}, observation.ToolProgress{}, observation.ToolFinished{}, observation.RoundFinished{},
		observation.RoundStarted{}, observation.ModelStarted{}, observation.ModelProgress{}, observation.ModelFinished{}, observation.RoundFinished{},
		observation.TurnFinished{},
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("event count=%d want=%d events=%#v", len(events), len(wantTypes), events)
	}
	turnID := events[0].Correlation.TurnID
	if turnID == "" {
		t.Fatal("empty turn id")
	}
	for i, event := range events {
		if event.Sequence != uint64(i+1) || event.Correlation.SessionID != "s" || event.Correlation.TurnID != turnID {
			t.Fatalf("event %d metadata=%#v", i, event)
		}
		if typeName(event.Detail) != typeName(wantTypes[i]) {
			t.Fatalf("event %d detail=%T want=%T", i, event.Detail, wantTypes[i])
		}
	}
	for _, index := range []int{1, 2, 3, 4, 5, 6, 7, 8} {
		if events[index].Correlation.RoundIndex != 0 {
			t.Fatalf("event %d round=%d", index, events[index].Correlation.RoundIndex)
		}
	}
	for _, index := range []int{9, 10, 11, 12, 13} {
		if events[index].Correlation.RoundIndex != 1 {
			t.Fatalf("event %d round=%d", index, events[index].Correlation.RoundIndex)
		}
	}
	for _, index := range []int{5, 6, 7} {
		if events[index].Correlation.ToolCallID != "call-1" {
			t.Fatalf("event %d tool call=%q", index, events[index].Correlation.ToolCallID)
		}
	}
	if finished := events[7].Detail.(observation.ToolFinished); finished.Status != observation.StatusSucceeded || finished.Result == nil {
		t.Fatalf("tool finished=%#v", finished)
	}
	if finished := events[8].Detail.(observation.RoundFinished); finished.Status != observation.StatusSucceeded || finished.Result == nil || len(finished.Result.ToolMessages) != 1 {
		t.Fatalf("round finished=%#v", finished)
	}
	if finished := events[14].Detail.(observation.TurnFinished); finished.Status != observation.StatusSucceeded || finished.Result == nil {
		t.Fatalf("turn finished=%#v", finished)
	}
}

func TestPostDispatchToolFailureStopsExecutionAndMarksScopesFailed(t *testing.T) {
	consumer := &recordingConsumer{}
	models := &sequenceModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "call-1", Name: "echo", Arguments: json.RawMessage(`{}`)}}}},
		{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("done")}},
	}}
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: models, Tools: &progressTools{observation: consumer, err: errors.New("tool failed")},
		Store: &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}, Assets: newMemoryAssets(),
		Prompt: passthroughPrompt{}, Observation: consumer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"}); err == nil {
		t.Fatal("tool error did not stop the turn")
	}
	var toolFinished observation.ToolFinished
	var firstRound observation.RoundFinished
	for _, event := range consumer.snapshot() {
		switch detail := event.Detail.(type) {
		case observation.ToolFinished:
			toolFinished = detail
		case observation.RoundFinished:
			if event.Correlation.RoundIndex == 0 {
				firstRound = detail
			}
		}
	}
	if toolFinished.Status != observation.StatusFailed || toolFinished.Result != nil || toolFinished.Error == "" {
		t.Fatalf("tool finished=%#v", toolFinished)
	}
	if firstRound.Status != observation.StatusFailed || firstRound.Result != nil || firstRound.Error == "" {
		t.Fatalf("round finished=%#v", firstRound)
	}
}

func TestRoundPolicyRejectsAfterSuccessfulModelObservation(t *testing.T) {
	consumer := &recordingConsumer{}
	rejected := errors.New("round rejected")
	interceptor := roundInterceptorFunc(func(context.Context, agent.Round, pipeline.Next[agent.Round, agent.RoundResult]) (agent.RoundResult, error) {
		return agent.RoundResult{}, rejected
	})
	models := &sequenceModel{responses: []model.Response{{Message: model.Message{
		Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "call-1", Name: "echo", Arguments: json.RawMessage(`{}`)}},
	}}}}
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: models, Tools: &progressTools{observation: consumer}, Store: &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}},
		Assets: newMemoryAssets(), Prompt: passthroughPrompt{}, Observation: consumer,
		RoundInterceptors: []agent.RoundInterceptor{interceptor},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"}); !errors.Is(err, rejected) {
		t.Fatalf("error=%v", err)
	}
	modelSucceeded := false
	roundFailed := false
	turnFailed := false
	for _, event := range consumer.snapshot() {
		switch detail := event.Detail.(type) {
		case observation.ModelFinished:
			modelSucceeded = detail.Status == observation.StatusSucceeded && detail.Response != nil
		case observation.RoundFinished:
			roundFailed = detail.Status == observation.StatusFailed && detail.Result == nil
		case observation.TurnFinished:
			turnFailed = detail.Status == observation.StatusFailed && detail.Result == nil
		case observation.ToolStarted:
			t.Fatalf("tool started after policy reject: %#v", detail)
		}
	}
	if !modelSucceeded || !roundFailed || !turnFailed {
		t.Fatalf("modelSucceeded=%v roundFailed=%v turnFailed=%v", modelSucceeded, roundFailed, turnFailed)
	}
}

func TestPersistenceFailureDoesNotRewriteSuccessfulToolExecution(t *testing.T) {
	consumer := &recordingConsumer{}
	persistErr := errors.New("persist failed")
	store := &failingAppendStore{
		memoryStore: &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}},
		failAt:      3,
		err:         persistErr,
	}
	models := &sequenceModel{responses: []model.Response{{Message: model.Message{
		Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "call-1", Name: "echo", Arguments: json.RawMessage(`{}`)}},
	}}}}
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: models, Tools: &progressTools{observation: consumer}, Store: store,
		Assets: newMemoryAssets(), Prompt: passthroughPrompt{}, Observation: consumer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"}); !errors.Is(err, persistErr) {
		t.Fatalf("error=%v", err)
	}
	toolSucceeded := false
	roundFailed := false
	turnFailed := false
	for _, event := range consumer.snapshot() {
		switch detail := event.Detail.(type) {
		case observation.ToolFinished:
			toolSucceeded = detail.Status == observation.StatusSucceeded && detail.Result != nil
		case observation.RoundFinished:
			roundFailed = detail.Status == observation.StatusFailed && detail.Result == nil
		case observation.TurnFinished:
			turnFailed = detail.Status == observation.StatusFailed && detail.Result == nil
		}
	}
	if !toolSucceeded || !roundFailed || !turnFailed {
		t.Fatalf("toolSucceeded=%v roundFailed=%v turnFailed=%v", toolSucceeded, roundFailed, turnFailed)
	}
}

func TestExecutionPanicStillFinishesStartedScopesAndPropagates(t *testing.T) {
	consumer := &recordingConsumer{}
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: panicModel{}, Tools: &fakeTools{}, Store: &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}},
		Assets: newMemoryAssets(), Prompt: passthroughPrompt{}, Observation: consumer,
	})
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recovered := recover(); recovered != "model panic" {
				t.Fatalf("panic=%#v", recovered)
			}
		}()
		_, _ = exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
	}()
	events := consumer.snapshot()
	want := []any{
		observation.TurnStarted{}, observation.RoundStarted{}, observation.ModelStarted{},
		observation.ModelFinished{}, observation.RoundFinished{}, observation.TurnFinished{},
	}
	if len(events) != len(want) {
		t.Fatalf("events=%#v", events)
	}
	for i, event := range events {
		if typeName(event.Detail) != typeName(want[i]) {
			t.Fatalf("event %d=%T want=%T", i, event.Detail, want[i])
		}
		switch detail := event.Detail.(type) {
		case observation.ModelFinished:
			if detail.Status != observation.StatusFailed || detail.Error != "model panic" {
				t.Fatalf("model finished=%#v", detail)
			}
		case observation.RoundFinished:
			if detail.Status != observation.StatusFailed || detail.Error != "model panic" {
				t.Fatalf("round finished=%#v", detail)
			}
		case observation.TurnFinished:
			if detail.Status != observation.StatusFailed || detail.Error != "model panic" {
				t.Fatalf("turn finished=%#v", detail)
			}
		}
	}
}

func typeName(value any) string {
	return fmt.Sprintf("%T", value)
}

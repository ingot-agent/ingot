package observationcomponent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	agentdefault "github.com/ingot-agent/agent-default"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/tool"
)

type observerFunc func(observation.Event)

func (f observerFunc) Observe(event observation.Event) { f(event) }

func TestHubSequencesClonesAndIsolatesObservers(t *testing.T) {
	var mu sync.Mutex
	var second []observation.Event
	mutating := observerFunc(func(event observation.Event) {
		switch detail := event.Detail.(type) {
		case observation.TurnStarted:
			started := detail
			started.Turn.Attachments[0].Media.Source.Data[0] = 9
		case observation.ModelStarted:
			detail.Request.Messages[0].Content[0].Text = "mutated"
			detail.Request.Tools[0].InputSchema[0] = '['
		case observation.ModelProgress:
			detail.Progress.DataDelta[0] = 9
		case observation.ToolStarted:
			detail.Call.Arguments[0] = '['
		case observation.ToolProgress:
			detail.Progress.Content[0].Media.Source.Data[0] = 9
		case observation.ModelFinished:
			detail.Response.Message.Content[0].Text = "mutated"
		case observation.ToolFinished:
			detail.Result.Content[0].Text = "mutated"
		case observation.RoundFinished:
			detail.Result.Decision.Content[0].Text = "mutated"
		}
		panic("observer failure")
	})
	recording := observerFunc(func(event observation.Event) {
		mu.Lock()
		second = append(second, event)
		mu.Unlock()
	})
	exports, cleanup, err := New(context.Background(), agentdefault.Config{}, Dependencies{Observers: []observation.Observer{mutating, recording}})
	if err != nil {
		t.Fatal(err)
	}
	correlation := observation.Correlation{SessionID: "s", TurnID: "turn"}
	ctx := observation.WithCorrelation(context.Background(), correlation)
	data := []byte{1, 2, 3}
	turn := agent.Turn{SessionID: "s", Attachments: []content.Attachment{{
		Kind: content.KindImage, Media: content.Inline(content.KindImage, "image/png", "x", data).Media,
	}}}
	exports.Consumer.Emit(ctx, observation.TurnStarted{Turn: turn})
	data[0] = 8
	exports.Consumer.Emit(ctx, observation.ModelStarted{Request: model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: content.FromText("request")}},
		Tools:    []tool.Definition{{Name: "echo", InputSchema: json.RawMessage(`{}`)}},
	}})
	exports.Consumer.Emit(ctx, observation.ModelProgress{Progress: model.StreamEvent{DataDelta: []byte{1}}})
	exports.Consumer.Emit(ctx, observation.ToolStarted{Call: tool.Call{Arguments: json.RawMessage(`{}`)}})
	exports.Consumer.Emit(ctx, observation.ToolProgress{Progress: tool.Progress{Content: content.Content{
		content.Inline(content.KindFile, "application/octet-stream", "progress", []byte{1}),
	}}})
	exports.Consumer.Emit(ctx, observation.ModelFinished{Status: observation.StatusSucceeded, Response: &model.Response{Message: model.Message{Content: content.FromText("response")}}})
	exports.Consumer.Emit(ctx, observation.ToolFinished{Status: observation.StatusSucceeded, Result: &tool.Result{Content: content.FromText("result")}})
	exports.Consumer.Emit(ctx, observation.RoundFinished{Status: observation.StatusSucceeded, Result: &agent.RoundResult{Decision: model.Message{Content: content.FromText("decision")}}})
	exports.Consumer.Emit(ctx, observation.TurnFinished{Status: observation.StatusSucceeded, Result: &agent.Result{Output: content.FromText("ok")}})
	if err := cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(second) != 9 || second[0].Sequence != 1 || second[8].Sequence != 9 {
		t.Fatalf("events=%#v", second)
	}
	started := second[0].Detail.(observation.TurnStarted)
	if started.Turn.Attachments[0].Media.Source.Data[0] != 1 {
		t.Fatalf("observer snapshots aliased: %#v", started)
	}
	if got := second[1].Detail.(observation.ModelStarted); got.Request.Messages[0].Content[0].Text != "request" || got.Request.Tools[0].InputSchema[0] != '{' {
		t.Fatalf("model request snapshots aliased: %#v", got)
	}
	if second[2].Detail.(observation.ModelProgress).Progress.DataDelta[0] != 1 || second[3].Detail.(observation.ToolStarted).Call.Arguments[0] != '{' {
		t.Fatal("progress or call snapshots aliased")
	}
	if second[4].Detail.(observation.ToolProgress).Progress.Content[0].Media.Source.Data[0] != 1 ||
		second[5].Detail.(observation.ModelFinished).Response.Message.Content[0].Text != "response" ||
		second[6].Detail.(observation.ToolFinished).Result.Content[0].Text != "result" ||
		second[7].Detail.(observation.RoundFinished).Result.Decision.Content[0].Text != "decision" {
		t.Fatal("terminal or tool progress snapshots aliased")
	}
}

func TestHubDoesNotGateEmitOnObserver(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	observer := observerFunc(func(observation.Event) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
	})
	exports, cleanup, err := New(context.Background(), agentdefault.Config{}, Dependencies{Observers: []observation.Observer{observer}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := observation.WithCorrelation(context.Background(), observation.Correlation{TurnID: "turn"})
	exports.Consumer.Emit(ctx, observation.TurnStarted{})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("observer was not invoked")
	}
	emitted := make(chan struct{})
	go func() {
		exports.Consumer.Emit(ctx, observation.RoundStarted{})
		close(emitted)
	}()
	select {
	case <-emitted:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Emit waited for observer behavior")
	}
	close(release)
	if err := cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHubSequencesAreIndependentPerTurn(t *testing.T) {
	var mu sync.Mutex
	var events []observation.Event
	recording := observerFunc(func(event observation.Event) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	})
	exports, cleanup, err := New(context.Background(), agentdefault.Config{}, Dependencies{Observers: []observation.Observer{recording}})
	if err != nil {
		t.Fatal(err)
	}
	ctxA := observation.WithCorrelation(context.Background(), observation.Correlation{TurnID: "a"})
	ctxB := observation.WithCorrelation(context.Background(), observation.Correlation{TurnID: "b"})
	exports.Consumer.Emit(ctxA, observation.TurnStarted{})
	exports.Consumer.Emit(ctxB, observation.TurnStarted{})
	exports.Consumer.Emit(ctxA, observation.TurnFinished{Status: observation.StatusSucceeded, Result: &agent.Result{}})
	exports.Consumer.Emit(ctxB, observation.TurnFinished{Status: observation.StatusSucceeded, Result: &agent.Result{}})
	if err := cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 4 || events[0].Sequence != 1 || events[1].Sequence != 1 || events[2].Sequence != 2 || events[3].Sequence != 2 {
		t.Fatalf("events=%#v", events)
	}
	for i, event := range events {
		if event.Time.IsZero() {
			t.Fatalf("event %d has zero materialization time", i)
		}
	}
}

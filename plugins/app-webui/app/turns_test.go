package appcomponent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	appbackend "github.com/ingot-agent/app-webui"
	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
)

type streamAgentFunc func(context.Context, agent.Turn, agent.StreamHandler) (agent.Execution, error)

func (f streamAgentFunc) Stream(ctx context.Context, turn agent.Turn, handler agent.StreamHandler) (agent.Execution, error) {
	return f(ctx, turn, handler)
}

func TestAgentCapabilitiesAreIndependent(t *testing.T) {
	history := &testAgent{}
	stream := streamAgentFunc(func(context.Context, agent.Turn, agent.StreamHandler) (agent.Execution, error) {
		return agent.Execution{}, nil
	})
	c, err := newAgentController(ingotabi.None[agent.Runtime](), ingotabi.Some[agent.StreamingRuntime](stream), history)
	if err != nil || c.Capabilities().Run || !c.Capabilities().Stream {
		t.Fatalf("stream-only capability = %v, %v", c, err)
	}
	if _, err := c.Run(context.Background(), agent.Turn{}); !errors.Is(err, appbackend.ErrCapabilityUnavailable) {
		t.Fatalf("Run fallback = %v", err)
	}
	if _, err := newAgentController(ingotabi.None[agent.Runtime](), ingotabi.None[agent.StreamingRuntime](), history); !errors.Is(err, appbackend.ErrInvalidConfig) {
		t.Fatalf("missing runtimes = %v", err)
	}
	var missing *testAgent
	if _, err := newAgentController(ingotabi.Some[agent.Runtime](missing), ingotabi.Some[agent.StreamingRuntime](stream), history); !errors.Is(err, appbackend.ErrInvalidConfig) {
		t.Fatalf("typed nil = %v", err)
	}
}

func TestStreamingTurnSnapshotRevisionAndReplay(t *testing.T) {
	a := testApplication(t)
	ready, advance, advanced := make(chan struct{}), make(chan struct{}), make(chan struct{})
	stream := streamAgentFunc(func(ctx context.Context, turn agent.Turn, handler agent.StreamHandler) (agent.Execution, error) {
		for _, event := range []agent.StreamEvent{{Kind: agent.StreamReasoningDelta, TextDelta: "thinking"}, {Kind: agent.StreamOutputDelta, TextDelta: "hello"}} {
			if err := handler(event); err != nil {
				return agent.Execution{}, err
			}
		}
		close(ready)
		select {
		case <-advance:
		case <-ctx.Done():
			return agent.Execution{}, ctx.Err()
		}
		if err := handler(agent.StreamEvent{Kind: agent.StreamOutputDelta, TextDelta: " world"}); err != nil {
			return agent.Execution{}, err
		}
		close(advanced)
		<-ctx.Done()
		return agent.Execution{Outcome: agent.Outcome{Status: agent.OutcomeCanceled}}, ctx.Err()
	})
	controller, err := newAgentController(ingotabi.None[agent.Runtime](), ingotabi.Some[agent.StreamingRuntime](stream), &testAgent{})
	if err != nil {
		t.Fatal(err)
	}
	a.turns.agent = controller
	cursor := a.backend.Events().Cursor()
	id, err := a.turns.Start(agent.Turn{SessionID: "session", Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("stream did not produce progress")
	}
	snapshots := a.turns.Snapshots()
	if len(snapshots) != 1 || snapshots[0].Revision != 2 || snapshots[0].Output != "hello" || snapshots[0].Reasoning != "thinking" {
		t.Fatalf("partial snapshot = %#v", snapshots)
	}
	sub, err := a.backend.Events().Subscribe(cursor)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	current := snapshots[0]
	reduce := func(record appbackend.EventRecord) {
		var event struct {
			Type string
			Data struct {
				Revision uint64
				Text     string
			}
		}
		if err := json.Unmarshal(record.Data, &event); err != nil {
			t.Fatal(err)
		}
		if event.Data.Revision <= current.Revision {
			return
		}
		if event.Type == "agent.output.delta" {
			current.Output += event.Data.Text
		} else if event.Type == "agent.reasoning.delta" {
			current.Reasoning += event.Data.Text
		}
		current.Revision = event.Data.Revision
	}
	for _, record := range sub.Replay() {
		reduce(record)
	}
	if current.Output != "hello" {
		t.Fatal("bootstrap replay duplicated output")
	}
	close(advance)
	select {
	case <-advanced:
	case <-time.After(time.Second):
		t.Fatal("stream did not advance")
	}
	select {
	case record := <-sub.Events():
		reduce(record)
	case <-time.After(time.Second):
		t.Fatal("live delta missing")
	}
	if current.Revision != 3 || current.Output != "hello world" {
		t.Fatalf("live projection = %#v", current)
	}
	if err := a.turns.Cancel(id); err != nil {
		t.Fatal(err)
	}
}

func TestStreamingFailureDoesNotRetryRun(t *testing.T) {
	a := testApplication(t)
	called := false
	runtime := &testAgent{run: func(context.Context, agent.Turn) (agent.Execution, error) {
		called = true
		return agent.Execution{Result: &agent.Result{Output: content.FromText("unexpected")}}, nil
	}}
	stream := streamAgentFunc(func(context.Context, agent.Turn, agent.StreamHandler) (agent.Execution, error) {
		return agent.Execution{}, model.ErrStreamingUnsupported
	})
	controller, err := newAgentController(ingotabi.Some[agent.Runtime](runtime), ingotabi.Some[agent.StreamingRuntime](stream), runtime)
	if err != nil {
		t.Fatal(err)
	}
	a.turns.agent = controller
	if _, err := a.turns.Start(agent.Turn{SessionID: "session"}); err != nil {
		t.Fatal(err)
	}
	a.turns.stop()
	<-a.turns.done
	if called {
		t.Fatal("Stream failure retried Runtime.Run")
	}
}

func TestTurnCancellationAndTerminalEvent(t *testing.T) {
	a := testApplication(t)
	started := make(chan struct{})
	runtime := &testAgent{run: func(ctx context.Context, _ agent.Turn) (agent.Execution, error) {
		close(started)
		<-ctx.Done()
		return agent.Execution{}, ctx.Err()
	}}
	controller, err := newAgentController(ingotabi.Some[agent.Runtime](runtime), ingotabi.None[agent.StreamingRuntime](), runtime)
	if err != nil {
		t.Fatal(err)
	}
	a.turns.agent = controller
	id, err := a.turns.Start(agent.Turn{SessionID: "session", Input: "input"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := a.turns.Cancel(id); err != nil {
		t.Fatal(err)
	}
	a.turns.stop()
	select {
	case <-a.turns.done:
	case <-time.After(time.Second):
		t.Fatal("turn did not finish")
	}
	if len(a.turns.Snapshots()) != 0 {
		t.Fatal("terminal turn was retained")
	}
	if err := a.turns.Cancel(id); !errors.Is(err, appbackend.ErrTurnNotFound) {
		t.Fatalf("late cancel = %v", err)
	}
	if _, err := a.turns.Start(agent.Turn{SessionID: "session", Input: "late"}); !errors.Is(err, appbackend.ErrApplicationClosed) {
		t.Fatalf("start during shutdown = %v", err)
	}
	sub, err := a.backend.Events().Subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	records := sub.Replay()
	if len(records) != 2 {
		t.Fatalf("turn emitted %d events", len(records))
	}
	var finished struct {
		Type string `json:"type"`
		Data struct {
			InvocationID string `json:"invocationId"`
			Status       string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(records[1].Data, &finished); err != nil {
		t.Fatal(err)
	}
	if finished.Type != "agent.invocation.finished" || finished.Data.InvocationID != id || finished.Data.Status != "canceled" {
		t.Fatalf("finished = %#v", finished)
	}
}

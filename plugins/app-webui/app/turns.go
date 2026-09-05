package appcomponent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	appbackend "github.com/ingot-agent/app-webui"
	"github.com/ingot-agent/app-webui/internal/projection"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/session"
)

type turnInvocation struct {
	id        string
	sessionID session.ID
	cancel    context.CancelFunc
	revision  uint64
	reasoning string
	output    string
}

type turnRegistry struct {
	mu      sync.Mutex
	nextID  atomic.Uint64
	appCtx  context.Context
	agent   agentController
	events  appbackend.EventSink
	running map[string]*turnInvocation
	closed  bool
	done    chan struct{}
}

func newTurnRegistry(ctx context.Context, controller agentController, events appbackend.EventSink) *turnRegistry {
	return &turnRegistry{appCtx: ctx, agent: controller, events: events, running: make(map[string]*turnInvocation), done: make(chan struct{})}
}

func (r *turnRegistry) Start(turn agent.Turn) (string, error) {
	turn.Attachments = content.CloneAttachments(turn.Attachments)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.appCtx.Err() != nil {
		return "", appbackend.ErrApplicationClosed
	}
	id := fmt.Sprintf("turn-inv-%d", r.nextID.Add(1))
	ctx, cancel := context.WithCancel(r.appCtx)
	invocation := &turnInvocation{id: id, sessionID: turn.SessionID, cancel: cancel}
	r.running[id] = invocation
	if err := r.events.Publish(appbackend.Event{
		Type:  "agent.invocation.started",
		Scope: &appbackend.Scope{Agent: &appbackend.AgentScope{SessionID: string(turn.SessionID)}},
		Data:  appbackend.TurnSnapshot{ID: id, SessionID: string(turn.SessionID)},
	}); err != nil {
		delete(r.running, id)
		cancel()
		return "", err
	}
	go r.run(ctx, invocation, turn)
	return id, nil
}

func (r *turnRegistry) stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	for _, invocation := range r.running {
		invocation.cancel()
	}
	if len(r.running) == 0 {
		close(r.done)
	}
}

func (r *turnRegistry) Cancel(id string) error {
	r.mu.Lock()
	invocation, ok := r.running[id]
	r.mu.Unlock()
	if !ok {
		return appbackend.ErrTurnNotFound
	}
	invocation.cancel()
	return nil
}

func (r *turnRegistry) Snapshots() []appbackend.TurnSnapshot {
	r.mu.Lock()
	result := make([]appbackend.TurnSnapshot, 0, len(r.running))
	for _, invocation := range r.running {
		result = append(result, appbackend.TurnSnapshot{
			ID: invocation.id, SessionID: string(invocation.sessionID), Revision: invocation.revision,
			Reasoning: invocation.reasoning, Output: invocation.output,
		})
	}
	r.mu.Unlock()
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func executionStatus(err error) string {
	status := "succeeded"
	if err != nil {
		status = "failed"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			status = "canceled"
		}
	}
	return status
}

func (r *turnRegistry) run(ctx context.Context, invocation *turnInvocation, turn agent.Turn) {
	var result agent.Execution
	var err error
	if r.agent.Capabilities().Stream {
		result, err = r.agent.Stream(ctx, turn, func(event agent.StreamEvent) error { return r.delta(ctx, invocation, event) })
	} else {
		result, err = r.agent.Run(ctx, turn)
	}
	status := executionStatus(err)
	invocation.cancel()
	data := map[string]any{"invocationId": invocation.id, "status": status, "outcome": projection.Outcome(result.Outcome)}
	if err != nil {
		_, detail := apiError(err)
		data["error"] = detail
	}
	if err == nil && result.Result != nil {
		data["result"] = map[string]any{"output": projection.Content(result.Result.Output)}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.running, invocation.id)
	_ = r.events.Publish(appbackend.Event{
		Type:  "agent.invocation.finished",
		Scope: &appbackend.Scope{Agent: &appbackend.AgentScope{SessionID: string(invocation.sessionID)}},
		Data:  data,
	})
	if r.closed && len(r.running) == 0 {
		close(r.done)
	}
}

func (r *turnRegistry) delta(ctx context.Context, invocation *turnInvocation, event agent.StreamEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	eventType := ""
	switch event.Kind {
	case agent.StreamOutputDelta:
		eventType = "agent.output.delta"
	case agent.StreamReasoningDelta:
		eventType = "agent.reasoning.delta"
	default:
		return fmt.Errorf("unknown agent stream event kind %d", event.Kind)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running[invocation.id] != invocation {
		return appbackend.ErrTurnNotFound
	}
	revision := invocation.revision + 1
	if err := r.events.Publish(appbackend.Event{Type: eventType, Scope: &appbackend.Scope{Agent: &appbackend.AgentScope{SessionID: string(invocation.sessionID)}},
		Data: map[string]any{"invocationId": invocation.id, "revision": revision, "text": event.TextDelta}}); err != nil {
		return err
	}
	invocation.revision = revision
	if event.Kind == agent.StreamOutputDelta {
		invocation.output += event.TextDelta
	} else {
		invocation.reasoning += event.TextDelta
	}
	return nil
}

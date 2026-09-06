package hostcomponent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	appbackend "github.com/ingot-agent/app-webui"
	"github.com/ingot-agent/sdk/execution"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/session"
)

func TestExecutionBoundScopeAcrossSnapshotsAndEventsWithoutCorrelation(t *testing.T) {
	hub := newEventHub(32, 8)
	host := newInteractionHost(hub)
	channel, err := host.Bind(execution.Scope{SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, requestErr := channel.Request(context.Background(), interaction.Request{Name: "ask"})
		done <- requestErr
	}()
	pending := waitForPending(t, host)
	check := func(scope *appbackend.Scope) {
		t.Helper()
		if scope == nil || scope.Agent == nil || scope.Agent.SessionID != "session" || scope.Agent.TurnID != "" || scope.Agent.ToolCallID != "" || scope.Agent.RoundIndex != nil {
			t.Fatalf("execution scope = %#v", scope)
		}
	}
	check(pending.Scope)
	pending.Scope.Agent.SessionID = "changed"
	check(host.Pending()[0].Scope)
	if err := channel.Set(context.Background(), interaction.State{Name: "status"}); err != nil {
		t.Fatal(err)
	}
	check(host.States()[0].Scope)
	if err := channel.Emit(context.Background(), interaction.Event{Name: "notice"}); err != nil {
		t.Fatal(err)
	}
	if err := channel.Clear(context.Background(), "status"); err != nil {
		t.Fatal(err)
	}
	if err := host.Respond(pending.ID, appbackend.InteractionSubmission{}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	subscription, err := hub.Subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	for _, record := range subscription.Replay() {
		var event appbackend.Event
		if err := json.Unmarshal(record.Data, &event); err != nil {
			t.Fatal(err)
		}
		check(event.Scope)
	}
}

func TestExecutionBindingRejectsMissingSessionAndIgnoresConflictingCorrelation(t *testing.T) {
	hub := newEventHub(8, 2)
	host := newInteractionHost(hub)
	if _, err := host.Bind(execution.Scope{}); !errors.Is(err, interaction.ErrInvalidExecutionScope) {
		t.Fatalf("empty scope error = %v", err)
	}
	channel, err := host.Bind(execution.Scope{SessionID: "session-a"})
	if err != nil {
		t.Fatal(err)
	}
	conflicting := observation.WithCorrelation(context.Background(), observation.Correlation{
		SessionID: "session-b", TurnID: "wrong-turn", RoundIndex: 9, ToolCallID: "wrong-tool",
	})
	if err := channel.Emit(conflicting, interaction.Event{Name: "conflicting"}); err != nil {
		t.Fatal(err)
	}
	matching := observation.WithCorrelation(context.Background(), observation.Correlation{
		SessionID: "session-a", TurnID: "turn-a", RoundIndex: 2, ToolCallID: "tool-a",
	})
	if err := channel.Emit(matching, interaction.Event{Name: "matching"}); err != nil {
		t.Fatal(err)
	}
	subscription, err := hub.Subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	records := subscription.Replay()
	if len(records) != 2 {
		t.Fatalf("events = %d", len(records))
	}
	var events [2]appbackend.Event
	for i := range records {
		if err := json.Unmarshal(records[i].Data, &events[i]); err != nil {
			t.Fatal(err)
		}
	}
	conflictScope := events[0].Scope
	if conflictScope == nil || conflictScope.Agent == nil || conflictScope.Agent.SessionID != "session-a" || conflictScope.Agent.TurnID != "" || conflictScope.Agent.ToolCallID != "" || conflictScope.Agent.RoundIndex != nil {
		t.Fatalf("conflicting correlation changed explicit scope: %#v", conflictScope)
	}
	matchedScope := events[1].Scope
	if matchedScope == nil || matchedScope.Agent == nil || matchedScope.Agent.SessionID != "session-a" || matchedScope.Agent.TurnID != "turn-a" || matchedScope.Agent.ToolCallID != "tool-a" || matchedScope.Agent.RoundIndex == nil || *matchedScope.Agent.RoundIndex != 2 {
		t.Fatalf("matching correlation was not projected: %#v", matchedScope)
	}
}

func TestExplicitOperationScopeWinsAndOrdinaryChannelRemainsGlobal(t *testing.T) {
	host := newInteractionHost(newEventHub(16, 4))
	ctx := observation.WithCorrelation(context.Background(), observation.Correlation{SessionID: "session", TurnID: "turn"})
	if err := host.Scoped(appbackend.Scope{Operation: &appbackend.OperationScope{InvocationID: "operation"}}).Set(ctx, interaction.State{Name: "status"}); err != nil {
		t.Fatal(err)
	}
	state := host.States()[0]
	if state.Scope.Agent != nil || state.Scope.Operation.InvocationID != "operation" {
		t.Fatalf("scope = %#v", state.Scope)
	}
	if err := host.Set(ctx, interaction.State{Name: "status"}); err != nil {
		t.Fatal(err)
	}
	if host.States()[0].Scope != nil || len(host.States()) != 1 {
		t.Fatal("ordinary channel derived business scope from context correlation")
	}
}

func TestConcurrentExecutionBindingsDoNotCrossSessions(t *testing.T) {
	host := newInteractionHost(newEventHub(16, 4))
	ids := []string{"session-a", "session-b"}
	channels := make([]interaction.Channel, len(ids))
	for i, id := range ids {
		channel, err := host.Bind(execution.Scope{SessionID: session.ID(id)})
		if err != nil {
			t.Fatal(err)
		}
		channels[i] = channel
	}
	done := make(chan error, len(channels))
	for i, channel := range channels {
		go func(name string, scoped interaction.Channel) {
			_, requestErr := scoped.Request(context.Background(), interaction.Request{Name: "ask-" + name})
			done <- requestErr
		}(ids[i], channel)
	}
	pending := waitForPendingCount(t, host, len(channels))
	seen := map[string]bool{}
	for _, request := range pending {
		if request.Scope == nil || request.Scope.Agent == nil {
			t.Fatalf("missing execution scope: %#v", request)
		}
		seen[request.Scope.Agent.SessionID] = true
		if err := host.Respond(request.ID, appbackend.InteractionSubmission{}); err != nil {
			t.Fatal(err)
		}
	}
	if !seen["session-a"] || !seen["session-b"] || len(seen) != 2 {
		t.Fatalf("pending scopes = %#v", seen)
	}
	for range channels {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func waitForPendingCount(t *testing.T, host *interactionHost, count int) []appbackend.PendingInteraction {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if pending := host.Pending(); len(pending) == count {
			return pending
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %d pending interactions", count)
		case <-ticker.C:
		}
	}
}

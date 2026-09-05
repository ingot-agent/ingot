package hostcomponent

import (
	"context"
	"encoding/json"
	"testing"

	appbackend "github.com/ingot-agent/app-webui"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/observation"
)

func TestInteractionContextScopeAcrossSnapshotsAndEvents(t *testing.T) {
	hub := newEventHub(32, 8)
	host := newInteractionHost(hub)
	ctx := observation.WithCorrelation(context.Background(), observation.Correlation{
		SessionID: "session", TurnID: "sdk-turn", RoundIndex: 2, ToolCallID: "tool",
	})
	done := make(chan error, 1)
	go func() { _, err := host.Request(ctx, interaction.Request{Name: "ask"}); done <- err }()
	pending := waitForPending(t, host)
	check := func(scope *appbackend.Scope) {
		t.Helper()
		if scope == nil || scope.Agent == nil || scope.Agent.SessionID != "session" || scope.Agent.TurnID != "sdk-turn" || scope.Agent.ToolCallID != "tool" || scope.Agent.RoundIndex == nil || *scope.Agent.RoundIndex != 2 {
			t.Fatalf("missing context scope: %#v", scope)
		}
	}
	check(pending.Scope)
	pending.Scope.Agent.SessionID = "changed"
	check(host.Pending()[0].Scope)
	if err := host.Set(ctx, interaction.State{Name: "status"}); err != nil {
		t.Fatal(err)
	}
	check(host.States()[0].Scope)
	if host.States()[0].ID != "status" {
		t.Fatal("scope changed global state identity")
	}
	if err := host.Emit(ctx, interaction.Event{Name: "notice"}); err != nil {
		t.Fatal(err)
	}
	if err := host.Clear(ctx, "status"); err != nil {
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

func TestExplicitOperationScopeWinsOverContextAndGlobalIsPreserved(t *testing.T) {
	host := newInteractionHost(newEventHub(16, 4))
	ctx := observation.WithCorrelation(context.Background(), observation.Correlation{SessionID: "session", TurnID: "turn"})
	if err := host.Scoped(appbackend.Scope{Operation: &appbackend.OperationScope{InvocationID: "operation"}}).Set(ctx, interaction.State{Name: "status"}); err != nil {
		t.Fatal(err)
	}
	state := host.States()[0]
	if state.Scope.Agent != nil || state.Scope.Operation.InvocationID != "operation" {
		t.Fatalf("scope = %#v", state.Scope)
	}
	if err := host.Set(context.Background(), interaction.State{Name: "status"}); err != nil {
		t.Fatal(err)
	}
	if host.States()[0].Scope != nil || len(host.States()) != 1 {
		t.Fatal("global state identity changed")
	}
	if scope := contextScope(ctx); scope.Agent.RoundIndex != nil {
		t.Fatal("invented round presence for turn context")
	}
}

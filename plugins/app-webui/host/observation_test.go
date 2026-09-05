package hostcomponent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	appbackend "github.com/ingot-agent/app-webui"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/tool"
)

func TestObservationProjectsEveryDetailAndPreservesCorrelation(t *testing.T) {
	exports, _, err := New(context.Background(), appbackend.Config{}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 9, 5, 0, 0, 0, 123, time.UTC)
	details := []struct {
		name        string
		detail      observation.Detail
		round, tool bool
	}{
		{"agent.turn.started", observation.TurnStarted{Turn: agent.Turn{SessionID: "session", Input: "hi"}}, false, false},
		{"agent.round.started", &observation.RoundStarted{}, true, false},
		{"agent.model.started", observation.ModelStarted{Request: model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: content.FromText("hi")}}, Tools: []tool.Definition{{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}}}}, true, false},
		{"agent.model.progress", observation.ModelProgress{Progress: model.StreamEvent{Kind: model.StreamPartStart, Semantic: model.StreamSemanticReasoning, PartKind: content.KindText}}, true, false},
		{"agent.model.finished", observation.ModelFinished{Status: observation.StatusSucceeded, Response: &model.Response{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("answer")}, Usage: model.Usage{Reported: true}}}, true, false},
		{"agent.tool.started", observation.ToolStarted{Call: tool.Call{ID: "call", Name: "echo", Arguments: json.RawMessage(`{}`)}}, true, true},
		{"agent.tool.progress", observation.ToolProgress{Progress: tool.Progress{Channel: "log", Content: content.FromText("working")}}, true, true},
		{"agent.tool.finished", observation.ToolFinished{Status: observation.StatusFailed, Error: "failed"}, true, true},
		{"agent.round.finished", observation.RoundFinished{Status: observation.StatusSucceeded, Result: &agent.RoundResult{Decision: model.Message{Role: model.RoleAssistant, Content: content.FromText("answer")}}}, true, false},
		{"agent.turn.finished", observation.TurnFinished{Status: observation.StatusCanceled, Error: "canceled", Outcome: agent.Outcome{Status: agent.OutcomeCanceled, Accounting: agent.Accounting{Usage: agent.TokenUsage{Coverage: agent.UsagePartial}}}}, false, false},
	}
	for i, test := range details {
		exports.Observer.Observe(observation.Event{Time: when, Sequence: uint64(i + 1), Correlation: observation.Correlation{SessionID: "session", TurnID: "sdk-turn", RoundIndex: 0, ToolCallID: "call"}, Detail: test.detail})
	}
	sub, err := exports.Runtime.Events().Subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	records := sub.Replay()
	if len(records) != len(details) {
		t.Fatalf("observation events = %d", len(records))
	}
	for i, record := range records {
		var event struct {
			Type  string
			Scope appbackend.Scope
			Data  struct {
				Sequence uint64
				Time     time.Time
			}
		}
		if err := json.Unmarshal(record.Data, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type != details[i].name || event.Data.Sequence != uint64(i+1) || !event.Data.Time.Equal(when) || event.Scope.Agent.TurnID != "sdk-turn" {
			t.Fatalf("event = %s", record.Data)
		}
		if (event.Scope.Agent.RoundIndex != nil) != details[i].round || (event.Scope.Agent.ToolCallID != "") != details[i].tool {
			t.Fatalf("scope = %s", record.Data)
		}
		if details[i].round && *event.Scope.Agent.RoundIndex != 0 {
			t.Fatal("first round was lost")
		}
	}
	if !strings.Contains(string(records[3].Data), `"semantic":"reasoning"`) || !strings.Contains(string(records[3].Data), `"partKind":"text"`) || !strings.Contains(string(records[9].Data), `"coverage":"partial"`) {
		t.Fatal("SDK enum leaked or lost its meaning")
	}
	if len(exports.Runtime.Interactions().Pending()) != 0 || len(exports.Runtime.Interactions().States()) != 0 {
		t.Fatal("observation created authoritative interaction state")
	}
	var absent *observation.TurnStarted
	exports.Observer.Observe(observation.Event{Detail: absent})
	if exports.Runtime.Events().Cursor() != uint64(len(details)) {
		t.Fatal("nil observation detail was published")
	}
}

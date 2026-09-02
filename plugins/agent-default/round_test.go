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
	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/pipeline"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

type roundInterceptorFunc func(context.Context, agent.Round, pipeline.Next[agent.Round, agent.RoundResult]) (agent.RoundResult, error)

func (f roundInterceptorFunc) Invoke(ctx context.Context, round agent.Round, next pipeline.Next[agent.Round, agent.RoundResult]) (agent.RoundResult, error) {
	return f(ctx, round, next)
}

type tracedModel struct {
	trace     *[]string
	responses []model.Response
}

func (m *tracedModel) Complete(context.Context, model.Request) (model.Response, error) {
	*m.trace = append(*m.trace, "model")
	response := m.responses[0]
	m.responses = m.responses[1:]
	return response, nil
}

type tracedTools struct{ trace *[]string }

func (*tracedTools) Definitions() []tool.Definition {
	return []tool.Definition{{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}}
}

func (t *tracedTools) Call(context.Context, tool.Call) (tool.Result, error) {
	*t.trace = append(*t.trace, "tool")
	return tool.Result{Content: content.FromText("ok")}, nil
}

func TestRoundInterceptorOrderIncludesToolAndFinalRounds(t *testing.T) {
	var trace []string
	models := &tracedModel{trace: &trace, responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "a", Name: "echo", Arguments: json.RawMessage(`{}`)}}}},
		{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("done")}},
	}}
	interceptor := roundInterceptorFunc(func(ctx context.Context, round agent.Round, next pipeline.Next[agent.Round, agent.RoundResult]) (agent.RoundResult, error) {
		trace = append(trace, "round-before")
		result, err := next(ctx, round)
		trace = append(trace, "round-after")
		return result, err
	})
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: models, Tools: &tracedTools{trace: &trace}, Store: &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}},
		Assets: newMemoryAssets(), Prompt: passthroughPrompt{}, RoundInterceptors: []agent.RoundInterceptor{interceptor},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"model", "round-before", "tool", "round-after", "model", "round-before", "round-after"}
	if !reflect.DeepEqual(trace, want) {
		t.Fatalf("trace=%v want=%v", trace, want)
	}
}

func TestRoundInterceptorInspectsActualInvocationAndModifiesCanonicalDecision(t *testing.T) {
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	models := &sequenceModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("original"), ToolCalls: []tool.Call{
			{ID: "a", Name: "one", Arguments: json.RawMessage(`{"value":1}`)},
			{ID: "b", Name: "two", Arguments: json.RawMessage(`{"value":2}`)},
			{ID: "c", Name: "three", Arguments: json.RawMessage(`{"value":3}`)},
		}}},
		{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("done")}},
	}}
	tools := &fakeTools{}
	compactor := &recordingCompactor{outputs: [][]model.Message{
		{{Role: model.RoleUser, Content: content.FromText("actual invocation 0")}},
		{{Role: model.RoleUser, Content: content.FromText("actual invocation 1")}},
	}}
	var seen []agent.Round
	decisionWasIndependent := false
	interceptor := roundInterceptorFunc(func(ctx context.Context, round agent.Round, next pipeline.Next[agent.Round, agent.RoundResult]) (agent.RoundResult, error) {
		seen = append(seen, cloneRound(round))
		if round.Index == 0 {
			round.Decision.Content = content.FromText("canonical")
			round.Decision.ToolCalls = []tool.Call{cloneCall(round.Decision.ToolCalls[0]), cloneCall(round.Decision.ToolCalls[2])}
			round.Decision.ToolCalls[0].Name = "one-modified"
			round.Decision.ToolCalls[0].Arguments = json.RawMessage(`{"approved":true}`)
			decisionWasIndependent = round.Response.Message.ToolCalls[0].Name == "one" &&
				string(round.Response.Message.ToolCalls[0].Arguments) == `{"value":1}`
		}
		return next(ctx, round)
	})
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: models, Tools: tools, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{},
		Compactor: ingotabi.Some[contextwindow.Compactor](compactor), RoundInterceptors: []agent.RoundInterceptor{interceptor},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if textValue(executionOutput(result)) != "done" || !decisionWasIndependent || len(seen) != 2 || seen[0].Index != 0 || seen[1].Index != 1 {
		t.Fatalf("result=%#v independent=%v seen=%#v", result, decisionWasIndependent, seen)
	}
	if textValue(seen[0].Invocation.Messages[0].Content) != "actual invocation 0" ||
		textValue(seen[1].Invocation.Messages[0].Content) != "actual invocation 1" ||
		len(seen[0].Response.Message.ToolCalls) != 3 {
		t.Fatalf("round snapshots=%#v", seen)
	}
	if len(tools.calls) != 2 || tools.calls[0].ID != "a" || tools.calls[0].Name != "one-modified" ||
		string(tools.calls[0].Arguments) != `{"approved":true}` || tools.calls[1].ID != "c" {
		t.Fatalf("tool calls=%#v", tools.calls)
	}
	messages, err := exports.History.Load(context.Background(), "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 5 || textValue(messages[1].Content) != "canonical" || len(messages[1].ToolCalls) != 2 ||
		messages[1].ToolCalls[0].ID != "a" || messages[1].ToolCalls[1].ID != "c" {
		t.Fatalf("history=%#v", messages)
	}
}

func TestRoundInterceptorRejectsIllegalIdentityMutation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*agent.Round)
	}{
		{"session", func(round *agent.Round) { round.SessionID = "other" }},
		{"index", func(round *agent.Round) { round.Index++ }},
		{"invocation", func(round *agent.Round) { round.Invocation.Provider = "other" }},
		{"response", func(round *agent.Round) { round.Response.Message.Content = content.FromText("other") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
			models := &sequenceModel{responses: []model.Response{{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("original")}}}}
			interceptor := roundInterceptorFunc(func(ctx context.Context, round agent.Round, next pipeline.Next[agent.Round, agent.RoundResult]) (agent.RoundResult, error) {
				tc.mutate(&round)
				return next(ctx, round)
			})
			exports, _, err := New(context.Background(), Config{}, Dependencies{
				Model: models, Tools: &fakeTools{}, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{},
				RoundInterceptors: []agent.RoundInterceptor{interceptor},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
			if !errors.Is(err, ErrInvalidRound) || len(store.entries["s"]) != 1 {
				t.Fatalf("error=%v entries=%d", err, len(store.entries["s"]))
			}
		})
	}
}

func TestRoundInterceptorRejectsIllegalDecisionMutation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*model.Message)
	}{
		{"role", func(message *model.Message) { message.Role = model.RoleUser }},
		{"name", func(message *model.Message) { message.Name = "other" }},
		{"tool call id field", func(message *model.Message) { message.ToolCallID = "other" }},
		{"changed call id", func(message *model.Message) { message.ToolCalls[0].ID = "other" }},
		{"added call", func(message *model.Message) {
			message.ToolCalls = append(message.ToolCalls, tool.Call{ID: "added", Name: "echo", Arguments: json.RawMessage(`{}`)})
		}},
		{"duplicate call", func(message *model.Message) {
			message.ToolCalls = []tool.Call{message.ToolCalls[0], message.ToolCalls[0]}
		}},
		{"reordered calls", func(message *model.Message) {
			message.ToolCalls[0], message.ToolCalls[1] = message.ToolCalls[1], message.ToolCalls[0]
		}},
		{"empty call name", func(message *model.Message) { message.ToolCalls[0].Name = "" }},
		{"invalid arguments", func(message *model.Message) { message.ToolCalls[0].Arguments = json.RawMessage(`{`) }},
		{"invalid content", func(message *model.Message) { message.Content = content.Content{{Kind: content.Kind(255)}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
			models := &sequenceModel{responses: []model.Response{{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{
				{ID: "a", Name: "one", Arguments: json.RawMessage(`{}`)},
				{ID: "b", Name: "two", Arguments: json.RawMessage(`{}`)},
			}}}}}
			tools := &fakeTools{}
			interceptor := roundInterceptorFunc(func(ctx context.Context, round agent.Round, next pipeline.Next[agent.Round, agent.RoundResult]) (agent.RoundResult, error) {
				tc.mutate(&round.Decision)
				return next(ctx, round)
			})
			exports, _, err := New(context.Background(), Config{}, Dependencies{
				Model: models, Tools: tools, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{},
				RoundInterceptors: []agent.RoundInterceptor{interceptor},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
			if !errors.Is(err, ErrInvalidRoundDecision) || len(store.entries["s"]) != 1 || len(tools.calls) != 0 {
				t.Fatalf("error=%v entries=%d tools=%#v", err, len(store.entries["s"]), tools.calls)
			}
		})
	}
}

func TestRoundRejectAndShortCircuitHaveDistinctPersistence(t *testing.T) {
	t.Run("reject", func(t *testing.T) {
		policyErr := errors.New("policy rejected")
		store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
		tools := &fakeTools{}
		models := &sequenceModel{responses: []model.Response{{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "a", Name: "echo", Arguments: json.RawMessage(`{}`)}}}}}}
		interceptor := roundInterceptorFunc(func(context.Context, agent.Round, pipeline.Next[agent.Round, agent.RoundResult]) (agent.RoundResult, error) {
			return agent.RoundResult{}, policyErr
		})
		exports, _, err := New(context.Background(), Config{}, Dependencies{
			Model: models, Tools: tools, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{}, RoundInterceptors: []agent.RoundInterceptor{interceptor},
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
		if err != policyErr || len(store.entries["s"]) != 1 || len(tools.calls) != 0 {
			t.Fatalf("error=%v entries=%d tools=%#v", err, len(store.entries["s"]), tools.calls)
		}
	})

	t.Run("short circuit", func(t *testing.T) {
		store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
		tools := &fakeTools{}
		models := &sequenceModel{responses: []model.Response{{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "a", Name: "echo", Arguments: json.RawMessage(`{}`)}}}}}}
		interceptor := roundInterceptorFunc(func(context.Context, agent.Round, pipeline.Next[agent.Round, agent.RoundResult]) (agent.RoundResult, error) {
			return agent.RoundResult{Decision: model.Message{Role: model.RoleAssistant, Content: content.FromText("blocked")}}, nil
		})
		exports, _, err := New(context.Background(), Config{}, Dependencies{
			Model: models, Tools: tools, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{}, RoundInterceptors: []agent.RoundInterceptor{interceptor},
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
		if err != nil || textValue(executionOutput(result)) != "blocked" || len(store.entries["s"]) != 2 || len(tools.calls) != 0 {
			t.Fatalf("result=%#v error=%v entries=%d tools=%#v", result, err, len(store.entries["s"]), tools.calls)
		}
	})
}

func TestRoundRejectsInvalidShortCircuitResults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result agent.RoundResult
	}{
		{"tool calls", agent.RoundResult{Decision: model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "a", Name: "echo", Arguments: json.RawMessage(`{}`)}}}}},
		{"tool messages", agent.RoundResult{Decision: model.Message{Role: model.RoleAssistant}, ToolMessages: []model.Message{{Role: model.RoleTool, ToolCallID: "a"}}}},
		{"invalid decision", agent.RoundResult{Decision: model.Message{Role: model.RoleUser}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
			models := &sequenceModel{responses: []model.Response{{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("unused")}}}}
			interceptor := roundInterceptorFunc(func(context.Context, agent.Round, pipeline.Next[agent.Round, agent.RoundResult]) (agent.RoundResult, error) {
				return cloneRoundResult(tc.result), nil
			})
			exports, _, err := New(context.Background(), Config{}, Dependencies{
				Model: models, Tools: &fakeTools{}, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{}, RoundInterceptors: []agent.RoundInterceptor{interceptor},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
			if !errors.Is(err, ErrInvalidRoundResult) || len(store.entries["s"]) != 1 {
				t.Fatalf("error=%v entries=%d", err, len(store.entries["s"]))
			}
		})
	}
}

func TestRoundRejectsPostCommitResultMutation(t *testing.T) {
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	models := &sequenceModel{responses: []model.Response{{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "a", Name: "echo", Arguments: json.RawMessage(`{}`)}}}}}}
	tools := &fakeTools{}
	interceptor := roundInterceptorFunc(func(ctx context.Context, round agent.Round, next pipeline.Next[agent.Round, agent.RoundResult]) (agent.RoundResult, error) {
		result, err := next(ctx, round)
		result.Decision.Content = content.FromText("rewritten after commit")
		return result, err
	})
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: models, Tools: tools, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{}, RoundInterceptors: []agent.RoundInterceptor{interceptor},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
	if !errors.Is(err, ErrInvalidRoundResult) || len(store.entries["s"]) != 3 || len(tools.calls) != 1 {
		t.Fatalf("error=%v entries=%d tools=%#v", err, len(store.entries["s"]), tools.calls)
	}
}

func TestRoundTerminalCannotExecuteTwice(t *testing.T) {
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	models := &sequenceModel{responses: []model.Response{{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "a", Name: "echo", Arguments: json.RawMessage(`{}`)}}}}}}
	tools := &fakeTools{}
	interceptor := roundInterceptorFunc(func(ctx context.Context, round agent.Round, next pipeline.Next[agent.Round, agent.RoundResult]) (agent.RoundResult, error) {
		first, firstErr := next(ctx, round)
		_, _ = next(ctx, round)
		return first, firstErr
	})
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: models, Tools: tools, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{}, RoundInterceptors: []agent.RoundInterceptor{interceptor},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
	if !errors.Is(err, ErrInvalidRoundResult) || len(store.entries["s"]) != 3 || len(tools.calls) != 1 {
		t.Fatalf("error=%v entries=%d tools=%d", err, len(store.entries["s"]), len(tools.calls))
	}
}

func TestMaxRoundsCountsEveryModelInvocationAndChecksBeforeTools(t *testing.T) {
	toolResponse := func(id string) model.Response {
		return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("working"), ToolCalls: []tool.Call{{ID: id, Name: "echo", Arguments: json.RawMessage(`{}`)}}}}
	}
	finalResponse := model.Response{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("done")}}

	for _, tc := range []struct {
		name        string
		maxRounds   int
		responses   []model.Response
		wantErr     error
		wantEntries int
		wantTools   int
	}{
		{"one final round", 1, []model.Response{finalResponse}, nil, 2, 0},
		{"one tool round cannot continue", 1, []model.Response{toolResponse("a")}, ErrMaxRounds, 1, 0},
		{"tool then final", 2, []model.Response{toolResponse("a"), finalResponse}, nil, 4, 1},
		{"last round still requests tool", 2, []model.Response{toolResponse("a"), toolResponse("b")}, ErrMaxRounds, 3, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
			models := &sequenceModel{responses: append([]model.Response(nil), tc.responses...)}
			tools := &fakeTools{}
			exports, _, err := New(context.Background(), Config{MaxRounds: tc.maxRounds}, Dependencies{
				Model: models, Tools: tools, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
			if !errors.Is(err, tc.wantErr) || len(store.entries["s"]) != tc.wantEntries || len(tools.calls) != tc.wantTools {
				t.Fatalf("error=%v entries=%d tools=%d", err, len(store.entries["s"]), len(tools.calls))
			}
		})
	}

	t.Run("policy can make last round final", func(t *testing.T) {
		store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
		models := &sequenceModel{responses: []model.Response{toolResponse("a")}}
		tools := &fakeTools{}
		interceptor := roundInterceptorFunc(func(ctx context.Context, round agent.Round, next pipeline.Next[agent.Round, agent.RoundResult]) (agent.RoundResult, error) {
			round.Decision.Content = content.FromText("stopped before tool")
			round.Decision.ToolCalls = nil
			return next(ctx, round)
		})
		exports, _, err := New(context.Background(), Config{MaxRounds: 1}, Dependencies{
			Model: models, Tools: tools, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{}, RoundInterceptors: []agent.RoundInterceptor{interceptor},
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
		if err != nil || textValue(executionOutput(result)) != "stopped before tool" || len(store.entries["s"]) != 2 || len(tools.calls) != 0 {
			t.Fatalf("result=%#v error=%v entries=%d tools=%d", result, err, len(store.entries["s"]), len(tools.calls))
		}
	})
}

func TestRoundInterceptorStreamingRunsAfterProvisionalDeltas(t *testing.T) {
	policyErr := errors.New("stream policy rejected")
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	tools := &fakeTools{}
	streaming := modelStreamFunc(func(_ context.Context, _ model.Request, handler model.StreamHandler) (model.Response, error) {
		if err := handler(model.StreamEvent{Kind: model.StreamPartDelta, TextDelta: "provisional"}); err != nil {
			return model.Response{}, err
		}
		return model.Response{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "a", Name: "echo", Arguments: json.RawMessage(`{}`)}}}}, nil
	})
	interceptor := roundInterceptorFunc(func(context.Context, agent.Round, pipeline.Next[agent.Round, agent.RoundResult]) (agent.RoundResult, error) {
		return agent.RoundResult{}, policyErr
	})
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: &sequenceModel{}, Streaming: ingotabi.Some[model.StreamingRuntime](streaming), Tools: tools,
		Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{}, RoundInterceptors: []agent.RoundInterceptor{interceptor},
	})
	if err != nil {
		t.Fatal(err)
	}
	var events []agent.StreamEvent
	_, err = exports.Streaming.Stream(context.Background(), agent.Turn{SessionID: "s", Input: "hello"}, func(event agent.StreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != policyErr || !reflect.DeepEqual(events, []agent.StreamEvent{{Kind: agent.StreamOutputDelta, TextDelta: "provisional"}}) ||
		len(store.entries["s"]) != 1 || len(tools.calls) != 0 {
		t.Fatalf("error=%v events=%#v entries=%d tools=%d", err, events, len(store.entries["s"]), len(tools.calls))
	}
}

func TestRoundInterceptorDependenciesAreValidatedAndSnapshotted(t *testing.T) {
	var nilInterceptor roundInterceptorFunc
	_, _, err := New(context.Background(), Config{}, Dependencies{
		Model: &sequenceModel{}, Tools: &fakeTools{}, Store: &memoryStore{entries: map[session.ID][]session.Entry{}},
		Assets: newMemoryAssets(), Prompt: passthroughPrompt{}, RoundInterceptors: []agent.RoundInterceptor{nilInterceptor},
	})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("typed nil error=%v", err)
	}

	firstCalls, replacementCalls := 0, 0
	first := roundInterceptorFunc(func(ctx context.Context, round agent.Round, next pipeline.Next[agent.Round, agent.RoundResult]) (agent.RoundResult, error) {
		firstCalls++
		return next(ctx, round)
	})
	replacement := roundInterceptorFunc(func(ctx context.Context, round agent.Round, next pipeline.Next[agent.Round, agent.RoundResult]) (agent.RoundResult, error) {
		replacementCalls++
		return next(ctx, round)
	})
	interceptors := []agent.RoundInterceptor{first}
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: &sequenceModel{responses: []model.Response{{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("done")}}}},
		Tools: &fakeTools{}, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{}, RoundInterceptors: interceptors,
	})
	if err != nil {
		t.Fatal(err)
	}
	interceptors[0] = replacement
	if _, err := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"}); err != nil {
		t.Fatal(err)
	}
	if firstCalls != 1 || replacementCalls != 0 {
		t.Fatalf("first=%d replacement=%d", firstCalls, replacementCalls)
	}
}

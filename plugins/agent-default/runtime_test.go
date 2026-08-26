package agentdefault

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/ingot-agent/sdk"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/pipeline"
	"github.com/ingot-agent/sdk/prompt"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

type memoryStore struct {
	mu      sync.Mutex
	entries map[session.ID][]session.Entry
}

func (s *memoryStore) Create(context.Context, session.Metadata) (session.ID, error) {
	return "created", nil
}
func (s *memoryStore) Append(_ context.Context, id session.ID, entry session.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry.Payload = append(json.RawMessage(nil), entry.Payload...)
	s.entries[id] = append(s.entries[id], entry)
	return nil
}
func (s *memoryStore) Load(_ context.Context, id session.ID) ([]session.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := append([]session.Entry(nil), s.entries[id]...)
	for i := range entries {
		entries[i].Payload = append(json.RawMessage(nil), entries[i].Payload...)
	}
	return entries, nil
}
func (s *memoryStore) List(context.Context, session.Query) ([]session.Summary, error) {
	return nil, nil
}

type sequenceModel struct {
	mu        sync.Mutex
	responses []model.Response
	requests  []model.Request
}

type recordingCompactor struct {
	mu       sync.Mutex
	requests []contextwindow.CompactionRequest
	outputs  [][]model.Message
	err      error
}

func (c *recordingCompactor) Compact(_ context.Context, request contextwindow.CompactionRequest) (contextwindow.CompactionResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	request.Invocation = cloneModelRequest(request.Invocation)
	c.requests = append(c.requests, request)
	if c.err != nil {
		return contextwindow.CompactionResult{}, c.err
	}
	index := len(c.requests) - 1
	if index >= len(c.outputs) {
		return contextwindow.CompactionResult{Messages: cloneMessages(request.Invocation.Messages)}, nil
	}
	return contextwindow.CompactionResult{Messages: cloneMessages(c.outputs[index])}, nil
}

func (m *sequenceModel) Complete(_ context.Context, request model.Request) (model.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, request)
	response := m.responses[0]
	m.responses = m.responses[1:]
	return response, nil
}

type fakeTools struct{ calls []tool.Call }

func (t *fakeTools) Definitions() []tool.Definition {
	return []tool.Definition{{Name: "echo", Description: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}}
}
func (t *fakeTools) Call(_ context.Context, call tool.Call) (tool.Result, error) {
	t.calls = append(t.calls, cloneCall(call))
	return tool.Result{Content: "tool-ok"}, nil
}

type passthroughPrompt struct{}

func (passthroughPrompt) Render(_ context.Context, request prompt.Request) ([]model.Message, error) {
	result := cloneMessages(request.History)
	return append(result, model.Message{Role: model.RoleUser, Content: request.Input}), nil
}

type recordingInteraction struct{ events []interaction.Event }

type agentInterceptorFunc func(context.Context, agent.Turn, pipeline.Next[agent.Turn, agent.Result]) (agent.Result, error)

func (f agentInterceptorFunc) Invoke(ctx context.Context, turn agent.Turn, next pipeline.Next[agent.Turn, agent.Result]) (agent.Result, error) {
	return f(ctx, turn, next)
}

func (r *recordingInteraction) Ask(context.Context, interaction.AskRequest) (interaction.AskResponse, error) {
	return interaction.AskResponse{}, errors.New("unused")
}
func (r *recordingInteraction) Render(_ context.Context, event interaction.Event) error {
	r.events = append(r.events, event)
	return nil
}

func TestAgentRunsToolLoopAndPersistsExactOrder(t *testing.T) {
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	models := &sequenceModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{"x":1}`)}}}},
		{Message: model.Message{Role: model.RoleAssistant, Content: "done"}},
	}}
	tools := &fakeTools{}
	ui := &recordingInteraction{}
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: models, Tools: tools, Store: store, Prompt: passthroughPrompt{}, Interaction: sdk.Some[interaction.Channel](ui),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "done" || len(tools.calls) != 1 || len(models.requests) != 2 {
		t.Fatalf("result=%#v tool calls=%d model calls=%d", result, len(tools.calls), len(models.requests))
	}
	entries, _ := store.Load(context.Background(), "s")
	wantRoles := []model.Role{model.RoleUser, model.RoleAssistant, model.RoleTool, model.RoleAssistant}
	if len(entries) != len(wantRoles) {
		t.Fatalf("entries=%d", len(entries))
	}
	for i, entry := range entries {
		message, decodeErr := decodePersistedMessage(entry.Payload)
		if decodeErr != nil || message.Role != wantRoles[i] {
			t.Fatalf("entry %d message=%#v err=%v", i, message, decodeErr)
		}
	}
	if len(ui.events) != 3 {
		t.Fatalf("events=%#v", ui.events)
	}
}

func TestAgentValidatesCompleteResponseBeforeRendering(t *testing.T) {
	t.Parallel()
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	models := &sequenceModel{responses: []model.Response{{Message: model.Message{Role: model.RoleUser, Content: "must not render"}}}}
	ui := &recordingInteraction{}
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: models, Tools: &fakeTools{}, Store: store, Prompt: passthroughPrompt{}, Interaction: sdk.Some[interaction.Channel](ui),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
	if !errors.Is(err, ErrInvalidModelMessage) {
		t.Fatalf("error=%v", err)
	}
	if len(ui.events) != 0 {
		t.Fatalf("events=%#v, want none", ui.events)
	}
	entries, err := store.Load(context.Background(), "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries=%d, want committed user only", len(entries))
	}
	message, err := decodePersistedMessage(entries[0].Payload)
	if err != nil || message.Role != model.RoleUser || message.Content != "hello" {
		t.Fatalf("committed message=%#v error=%v", message, err)
	}
}

func TestAgentCompactsEveryModelInvocationWithoutReplacingRawMessages(t *testing.T) {
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	models := &sequenceModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{"x":1}`)}}}},
		{Message: model.Message{Role: model.RoleAssistant, Content: "done"}},
	}}
	compactor := &recordingCompactor{outputs: [][]model.Message{
		{{Role: model.RoleUser, Content: "compact-view-1"}},
		{{Role: model.RoleUser, Content: "compact-view-2"}},
	}}
	temperature := 0.25
	maxTokens := 321
	exports, _, err := New(context.Background(), Config{
		Provider: "provider", Model: "model", Temperature: &temperature, MaxTokens: &maxTokens,
	}, Dependencies{
		Model: models, Tools: &fakeTools{}, Store: store, Prompt: passthroughPrompt{},
		Compactor: sdk.Some[contextwindow.Compactor](compactor),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "done" {
		t.Fatalf("result=%#v", result)
	}

	compactor.mu.Lock()
	requests := append([]contextwindow.CompactionRequest(nil), compactor.requests...)
	compactor.mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("compactor calls=%d", len(requests))
	}
	first := requests[0]
	if first.SessionID != "s" || first.Invocation.Provider != "provider" || first.Invocation.Model != "model" ||
		first.Invocation.Temperature == nil || *first.Invocation.Temperature != temperature ||
		first.Invocation.MaxTokens == nil || *first.Invocation.MaxTokens != maxTokens || len(first.Invocation.Tools) != 1 {
		t.Fatalf("first compaction request=%#v", first)
	}
	secondMessages := requests[1].Invocation.Messages
	if len(secondMessages) != 3 || secondMessages[0].Content != "hello" ||
		secondMessages[1].Role != model.RoleAssistant || len(secondMessages[1].ToolCalls) != 1 ||
		secondMessages[2].Role != model.RoleTool || secondMessages[2].Content != "tool-ok" {
		t.Fatalf("second raw invocation messages=%#v", secondMessages)
	}
	if secondMessages[0].Content == "compact-view-1" {
		t.Fatal("compacted view replaced the agent's raw in-memory messages")
	}

	models.mu.Lock()
	modelRequests := append([]model.Request(nil), models.requests...)
	models.mu.Unlock()
	if len(modelRequests) != 2 || len(modelRequests[0].Messages) != 1 || modelRequests[0].Messages[0].Content != "compact-view-1" ||
		len(modelRequests[1].Messages) != 1 || modelRequests[1].Messages[0].Content != "compact-view-2" {
		t.Fatalf("model requests=%#v", modelRequests)
	}
}

func TestAgentCompactorErrorStopsModelAndPreservesCommittedUser(t *testing.T) {
	compactErr := errors.New("compact failed")
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	models := &sequenceModel{}
	compactor := &recordingCompactor{err: compactErr}
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: models, Tools: &fakeTools{}, Store: store, Prompt: passthroughPrompt{},
		Compactor: sdk.Some[contextwindow.Compactor](compactor),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
	if !errors.Is(err, compactErr) {
		t.Fatalf("error=%v", err)
	}
	models.mu.Lock()
	modelCalls := len(models.requests)
	models.mu.Unlock()
	if modelCalls != 0 {
		t.Fatalf("model calls=%d", modelCalls)
	}
	entries, err := store.Load(context.Background(), "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries=%d", len(entries))
	}
	message, err := decodePersistedMessage(entries[0].Payload)
	if err != nil || message.Role != model.RoleUser || message.Content != "hello" {
		t.Fatalf("committed message=%#v error=%v", message, err)
	}
}

func TestAgentRejectsTypedNilCompactor(t *testing.T) {
	var compactor *recordingCompactor
	_, _, err := New(context.Background(), Config{}, Dependencies{
		Model: &sequenceModel{}, Tools: &fakeTools{}, Store: &memoryStore{entries: map[session.ID][]session.Entry{}},
		Prompt: passthroughPrompt{}, Compactor: sdk.Some[contextwindow.Compactor](compactor),
	})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error=%v", err)
	}
}

func TestAgentRejectsNonFiniteTemperature(t *testing.T) {
	t.Parallel()
	for _, temperature := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		temperature := temperature
		_, _, err := New(context.Background(), Config{Temperature: &temperature}, Dependencies{
			Model: &sequenceModel{}, Tools: &fakeTools{}, Store: &memoryStore{entries: map[session.ID][]session.Entry{}}, Prompt: passthroughPrompt{},
		})
		if !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("temperature=%v error=%v", temperature, err)
		}
	}
}

func TestAgentRecoversTrailingToolRoundWithoutRetry(t *testing.T) {
	assistant := model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{
		{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{}`)},
		{ID: "c2", Name: "echo", Arguments: json.RawMessage(`{}`)},
	}}
	firstResult := model.Message{Role: model.RoleTool, Content: "ok", ToolCallID: "c1"}
	assistantPayload, _ := encodePersistedMessage(assistant)
	resultPayload, _ := encodePersistedMessage(firstResult)
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {
		{Kind: agentMessageKind, Version: agentMessageVersion, Payload: assistantPayload},
		{Kind: agentMessageKind, Version: agentMessageVersion, Payload: resultPayload},
	}}}
	models := &sequenceModel{responses: []model.Response{{Message: model.Message{Role: model.RoleAssistant, Content: "continued"}}}}
	tools := &fakeTools{}
	exports, _, err := New(context.Background(), Config{}, Dependencies{Model: models, Tools: tools, Store: store, Prompt: passthroughPrompt{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "next"}); err != nil {
		t.Fatal(err)
	}
	if len(tools.calls) != 0 {
		t.Fatalf("recovery retried tools: %d", len(tools.calls))
	}
	entries, _ := store.Load(context.Background(), "s")
	if len(entries) != 5 {
		t.Fatalf("entries=%d", len(entries))
	}
	recovered, err := decodePersistedMessage(entries[2].Payload)
	if err != nil || recovered.Role != model.RoleTool || recovered.ToolCallID != "c2" || recovered.Content != interruptedContent {
		t.Fatalf("recovered=%#v err=%v", recovered, err)
	}
}

type blockingModel struct {
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
	calls   int
}

func (m *blockingModel) Complete(ctx context.Context, _ model.Request) (model.Response, error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	if call == 1 {
		close(m.entered)
		select {
		case <-m.release:
		case <-ctx.Done():
			return model.Response{}, ctx.Err()
		}
	}
	return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: "ok"}}, nil
}

func TestAgentSerializesSameSession(t *testing.T) {
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	models := &blockingModel{entered: make(chan struct{}), release: make(chan struct{})}
	exports, _, err := New(context.Background(), Config{}, Dependencies{Model: models, Tools: &fakeTools{}, Store: store, Prompt: passthroughPrompt{}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() {
		_, runErr := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "one"})
		done <- runErr
	}()
	<-models.entered
	go func() {
		_, runErr := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "two"})
		done <- runErr
	}()
	time.Sleep(50 * time.Millisecond)
	models.mu.Lock()
	callsBeforeRelease := models.calls
	models.mu.Unlock()
	if callsBeforeRelease != 1 {
		t.Fatalf("same-session calls before release=%d", callsBeforeRelease)
	}
	close(models.release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgentRejectsInterceptorSessionIDRewriteBeforeTerminal(t *testing.T) {
	t.Parallel()
	store := &memoryStore{entries: map[session.ID][]session.Entry{"original": {}, "rewritten": {}}}
	models := &sequenceModel{responses: []model.Response{{Message: model.Message{Role: model.RoleAssistant, Content: "unused"}}}}
	rewrite := agentInterceptorFunc(func(ctx context.Context, turn agent.Turn, next pipeline.Next[agent.Turn, agent.Result]) (agent.Result, error) {
		turn.SessionID = "rewritten"
		return next(ctx, turn)
	})
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: models, Tools: &fakeTools{}, Store: store, Prompt: passthroughPrompt{}, Interceptors: []agent.Interceptor{rewrite},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "original", Input: "hello"})
	if !errors.Is(err, ErrInvalidTurn) {
		t.Fatalf("error=%v", err)
	}
	models.mu.Lock()
	modelCalls := len(models.requests)
	models.mu.Unlock()
	if modelCalls != 0 {
		t.Fatalf("model calls=%d, want 0", modelCalls)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.entries["original"]) != 0 || len(store.entries["rewritten"]) != 0 {
		t.Fatalf("entries=%#v", store.entries)
	}
}

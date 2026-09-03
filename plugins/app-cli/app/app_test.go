package appcomponent

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	appcli "github.com/ingot-agent/app-cli"
	"github.com/ingot-agent/ingot-abi/invocation"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
)

type fakeFrontend struct {
	mu         sync.Mutex
	lines      []string
	events     []interaction.Event
	views      []appcli.SessionView
	starts     []string
	finishes   []appcli.TurnState
	interrupts chan appcli.Interrupt
	block      bool
	readErr    error
	renderErr  error
}

func (f *fakeFrontend) Request(context.Context, interaction.Request) (interaction.Response, error) {
	return interaction.Response{}, errors.New("unused")
}

func (f *fakeFrontend) ReadLine(ctx context.Context, _ string) (string, error) {
	if f.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.lines) == 0 {
		if f.readErr != nil {
			return "", f.readErr
		}
		return "", io.EOF
	}
	line := f.lines[0]
	f.lines = f.lines[1:]
	return line, nil
}

func (f *fakeFrontend) Emit(_ context.Context, event interaction.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.renderErr != nil {
		return f.renderErr
	}
	f.events = append(f.events, event)
	return nil
}

func (*fakeFrontend) Set(context.Context, interaction.State) error { return nil }

func (*fakeFrontend) Clear(context.Context, string) error { return nil }

func (f *fakeFrontend) Sync(_ context.Context, view appcli.SessionView) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	view.Sessions = append([]session.Metadata(nil), view.Sessions...)
	view.Messages = append([]model.Message(nil), view.Messages...)
	f.views = append(f.views, view)
	return nil
}

func (f *fakeFrontend) StartTurn(_ context.Context, input string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, input)
	return nil
}

func (f *fakeFrontend) FinishTurn(_ context.Context, state appcli.TurnState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finishes = append(f.finishes, state)
	return nil
}

func (f *fakeFrontend) Interrupts() <-chan appcli.Interrupt { return f.interrupts }

type fakeAgent struct {
	mu       sync.Mutex
	turns    []agent.Turn
	err      error
	history  map[session.ID][]model.Message
	loaded   []session.ID
	loadErr  error
	blockRun bool
	output   string
}

func (f *fakeAgent) Run(ctx context.Context, turn agent.Turn) (agent.Execution, error) {
	f.mu.Lock()
	f.turns = append(f.turns, turn)
	block := f.blockRun
	err := f.err
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return agent.Execution{Outcome: agent.Outcome{Status: agent.OutcomeCanceled}}, ctx.Err()
	}
	if err != nil {
		return agent.Execution{Outcome: agent.Outcome{Status: agent.OutcomeFailed}}, err
	}
	output := f.output
	if output == "" {
		output = "assistant answer"
	}
	result := agent.Result{Output: content.FromText(output)}
	return agent.Execution{Result: &result, Outcome: agent.Outcome{Status: agent.OutcomeSucceeded}}, nil
}

func (f *fakeAgent) Load(_ context.Context, id session.ID) ([]model.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loaded = append(f.loaded, id)
	return append([]model.Message(nil), f.history[id]...), f.loadErr
}

type fakeStore struct {
	created   []session.CreateRequest
	createID  session.ID
	createErr error
	summaries []session.Metadata
	listErr   error
	renamed   []struct {
		id    session.ID
		title string
	}
	renameErr error
}

func (f *fakeStore) Create(_ context.Context, request session.CreateRequest) (session.Metadata, error) {
	f.created = append(f.created, request)
	if f.createErr != nil {
		return session.Metadata{}, f.createErr
	}
	if f.createID != "" {
		return session.Metadata{ID: f.createID, Title: request.Title}, nil
	}
	return session.Metadata{ID: "s1", Title: request.Title}, nil
}

func (*fakeStore) Append(context.Context, session.ID, session.Entry) error   { return nil }
func (*fakeStore) Load(context.Context, session.ID) ([]session.Entry, error) { return nil, nil }

func (f *fakeStore) List(context.Context) ([]session.Metadata, error) {
	return append([]session.Metadata(nil), f.summaries...), f.listErr
}

func (f *fakeStore) Get(_ context.Context, id session.ID) (session.Metadata, error) {
	return session.Metadata{ID: id}, nil
}

func (f *fakeStore) Rename(_ context.Context, id session.ID, title string) (session.Metadata, error) {
	f.renamed = append(f.renamed, struct {
		id    session.ID
		title string
	}{id: id, title: title})
	return session.Metadata{ID: id, Title: title}, f.renameErr
}

func (*fakeStore) Archive(_ context.Context, id session.ID) (session.Metadata, error) {
	return session.Metadata{ID: id}, nil
}

func (*fakeStore) Restore(_ context.Context, id session.ID) (session.Metadata, error) {
	return session.Metadata{ID: id}, nil
}

func (*fakeStore) Delete(context.Context, session.ID) error { return nil }

func (*fakeStore) Fork(_ context.Context, id session.ID, _ session.ForkRequest) (session.Metadata, error) {
	return session.Metadata{ID: id + "-fork"}, nil
}

type fakeModel struct {
	requests []model.Request
	response model.Response
	err      error
}

func (f *fakeModel) Complete(_ context.Context, request model.Request) (model.Response, error) {
	f.requests = append(f.requests, request)
	if f.err != nil {
		return model.Response{}, f.err
	}
	if f.response.Message.Role == "" {
		return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("Generated title")}}, nil
	}
	return f.response, nil
}

type fakeProcess struct {
	mu        sync.Mutex
	arguments []string
	mode      invocation.Mode
	requested bool
	err       error
	done      chan struct{}
}

func (f *fakeProcess) Arguments() []string   { return append([]string(nil), f.arguments...) }
func (f *fakeProcess) Mode() invocation.Mode { return f.mode }

func (f *fakeProcess) RequestShutdown(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.requested {
		return
	}
	f.requested = true
	f.err = err
	if f.done != nil {
		close(f.done)
	}
}

func testApplication(frontend *fakeFrontend, runtime *fakeAgent, store *fakeStore, process *fakeProcess) *application {
	return &application{
		agent: runtime, history: runtime, model: &fakeModel{}, interaction: frontend, frontend: frontend,
		store: store, manager: store, query: store, invocationData: process, lifecycle: process, inputPrompt: "> ",
	}
}

func testDependencies(frontend *fakeFrontend, runtime *fakeAgent, store *fakeStore, process *fakeProcess) Dependencies {
	return Dependencies{
		Agent: runtime, History: runtime, Model: &fakeModel{}, Interaction: frontend, Frontend: frontend,
		Store: store, Manager: store, Query: store, Invocation: process, Lifecycle: process,
	}
}

func TestLoopStartsBlankCreatesOnFirstSendAndExitsProcess(t *testing.T) {
	frontend := &fakeFrontend{lines: []string{"hello", "/exit"}}
	runtime := &fakeAgent{}
	store := &fakeStore{}
	process := &fakeProcess{}
	instance := testApplication(frontend, runtime, store, process)
	instance.initialTitle = "Initial"

	if err := instance.loop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(frontend.views) == 0 || frontend.views[0].Current != "" {
		t.Fatalf("startup view=%#v, want blank current session", frontend.views)
	}
	if len(store.created) != 1 || store.created[0].Title != "hello" {
		t.Fatalf("created=%#v", store.created)
	}
	if len(store.renamed) != 1 || store.renamed[0].id != "s1" || store.renamed[0].title != "Generated title" {
		t.Fatalf("renamed=%#v", store.renamed)
	}
	if len(runtime.turns) != 1 || runtime.turns[0].SessionID != "s1" {
		t.Fatalf("turns=%#v", runtime.turns)
	}
	if len(frontend.starts) != 1 || frontend.starts[0] != "hello" || len(frontend.finishes) != 1 || frontend.finishes[0] != appcli.TurnCompleted {
		t.Fatalf("starts=%#v finishes=%#v", frontend.starts, frontend.finishes)
	}
	if !process.requested || process.err != nil {
		t.Fatalf("shutdown requested=%v err=%v", process.requested, process.err)
	}
}

func TestNewReturnsPromptlyAndCleanupCancelsOnlyOwnedLoop(t *testing.T) {
	parent, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()
	process := &fakeProcess{}
	frontend := &fakeFrontend{block: true}
	runtime := &fakeAgent{}
	start := time.Now()
	_, cleanup, err := New(parent, appcli.Config{}, testDependencies(frontend, runtime, &fakeStore{}, process))
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("New blocked on CLI loop")
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := cleanup(cleanupCtx); err != nil {
		t.Fatal(err)
	}
	if parent.Err() != nil {
		t.Fatalf("component cleanup canceled parent: %v", parent.Err())
	}
}

func TestLoopCommandsProduceDeterministicTranscript(t *testing.T) {
	frontend := &fakeFrontend{lines: []string{"/new Work", "/list", "/use s2", "/help", "/exit"}}
	runtime := &fakeAgent{}
	store := &fakeStore{createID: "s1", summaries: []session.Metadata{{ID: "s1", Title: "Work"}, {ID: "s2", Title: "Other"}}}
	instance := testApplication(frontend, runtime, store, &fakeProcess{})

	if err := instance.loop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.created) != 1 || store.created[0].Title != "Work" || instance.current != "s2" {
		t.Fatalf("created=%#v current=%q", store.created, instance.current)
	}
	if len(runtime.loaded) == 0 || runtime.loaded[len(runtime.loaded)-1] != "s2" {
		t.Fatalf("loaded=%#v", runtime.loaded)
	}
	want := []string{
		"using session s1", "* s1  Work", "  s2  Other", "using session s2",
		"/new [title]  /rename <title>  /use <id>  /list  /help  /exit",
	}
	if len(frontend.events) != len(want) {
		t.Fatalf("events=%#v", frontend.events)
	}
	for index, event := range frontend.events {
		if event.Level != interaction.LevelInfo || event.Message != want[index] {
			t.Fatalf("event %d=%#v want=%q", index, event, want[index])
		}
	}
}

func TestLoopRendersAgentErrorAfterHistorySyncAndContinues(t *testing.T) {
	wantErr := errors.New("agent failed")
	frontend := &fakeFrontend{lines: []string{"hello", "/exit"}}
	runtime := &fakeAgent{err: wantErr}
	instance := testApplication(frontend, runtime, &fakeStore{}, &fakeProcess{})

	if err := instance.loop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(frontend.events) != 2 {
		t.Fatalf("events=%#v", frontend.events)
	}
	errorEvent := frontend.events[1]
	if errorEvent.Level != interaction.LevelError || !strings.Contains(errorEvent.Message, wantErr.Error()) {
		t.Fatalf("error event=%#v", frontend.events[1])
	}
	if len(frontend.finishes) != 1 || frontend.finishes[0] != appcli.TurnFailed {
		t.Fatalf("finishes=%#v", frontend.finishes)
	}
	if len(instance.store.(*fakeStore).renamed) != 0 {
		t.Fatalf("failed first turn renamed session: %#v", instance.store.(*fakeStore).renamed)
	}
}

func TestNewWithoutTitleWaitsForMessageAndRenameIsManual(t *testing.T) {
	frontend := &fakeFrontend{lines: []string{"/new", "next task", "/rename Project X", "/exit"}}
	runtime := &fakeAgent{}
	store := &fakeStore{}
	instance := testApplication(frontend, runtime, store, &fakeProcess{})

	if err := instance.loop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.created) != 1 || store.created[0].Title != "next task" {
		t.Fatalf("created=%#v", store.created)
	}
	if len(store.renamed) != 2 || store.renamed[0].title != "Generated title" || store.renamed[1].title != "Project X" {
		t.Fatalf("renamed=%#v", store.renamed)
	}
}

func TestAutomaticTitleFailureKeepsTemporaryTitle(t *testing.T) {
	frontend := &fakeFrontend{lines: []string{"first message", "/exit"}}
	runtime := &fakeAgent{}
	store := &fakeStore{}
	instance := testApplication(frontend, runtime, store, &fakeProcess{})
	instance.model = &fakeModel{err: errors.New("title unavailable")}

	if err := instance.loop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.created) != 1 || store.created[0].Title != "first message" || len(store.renamed) != 0 {
		t.Fatalf("created=%#v renamed=%#v", store.created, store.renamed)
	}
}

func TestAutomaticTitleRunsOnceAndNeverOverwritesManualNewTitle(t *testing.T) {
	t.Run("automatic", func(t *testing.T) {
		frontend := &fakeFrontend{lines: []string{"first topic", "follow up", "/exit"}}
		store := &fakeStore{}
		instance := testApplication(frontend, &fakeAgent{}, store, &fakeProcess{})
		titleModel := &fakeModel{}
		instance.model = titleModel
		if err := instance.loop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(titleModel.requests) != 1 || len(store.renamed) != 1 {
			t.Fatalf("title requests=%d renames=%#v", len(titleModel.requests), store.renamed)
		}
	})

	t.Run("manual", func(t *testing.T) {
		frontend := &fakeFrontend{lines: []string{"/new Project", "first topic", "/exit"}}
		store := &fakeStore{}
		instance := testApplication(frontend, &fakeAgent{}, store, &fakeProcess{})
		titleModel := &fakeModel{}
		instance.model = titleModel
		if err := instance.loop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(titleModel.requests) != 0 || len(store.renamed) != 0 || len(store.created) != 1 || store.created[0].Title != "Project" {
			t.Fatalf("title requests=%d created=%#v renames=%#v", len(titleModel.requests), store.created, store.renamed)
		}
	})
}

func TestTurnCanBeCanceledWithoutExiting(t *testing.T) {
	interrupts := make(chan appcli.Interrupt, 1)
	interrupts <- appcli.Interrupt{Kind: appcli.InterruptCancel}
	frontend := &fakeFrontend{lines: []string{"hello", "/exit"}, interrupts: interrupts}
	runtime := &fakeAgent{blockRun: true}
	process := &fakeProcess{}
	instance := testApplication(frontend, runtime, &fakeStore{}, process)

	if err := instance.loop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(frontend.finishes) != 1 || frontend.finishes[0] != appcli.TurnCanceled {
		t.Fatalf("finishes=%#v", frontend.finishes)
	}
	if !process.requested || process.err != nil {
		t.Fatalf("shutdown requested=%v err=%v", process.requested, process.err)
	}
}

func TestNewRejectsTypedNilDependenciesAndMissingProcess(t *testing.T) {
	var nilAgent *fakeAgent
	var nilModel *fakeModel
	var nilFrontend *fakeFrontend
	var nilStore *fakeStore
	validAgent := &fakeAgent{}
	validModel := &fakeModel{}
	validFrontend := &fakeFrontend{}
	tests := []Dependencies{
		{Agent: nilAgent, History: validAgent, Model: validModel, Interaction: validFrontend, Frontend: validFrontend, Store: &fakeStore{}, Manager: &fakeStore{}, Query: &fakeStore{}},
		{Agent: validAgent, History: nilAgent, Model: validModel, Interaction: validFrontend, Frontend: validFrontend, Store: &fakeStore{}, Manager: &fakeStore{}, Query: &fakeStore{}},
		{Agent: validAgent, History: validAgent, Model: nilModel, Interaction: validFrontend, Frontend: validFrontend, Store: &fakeStore{}, Manager: &fakeStore{}, Query: &fakeStore{}},
		{Agent: validAgent, History: validAgent, Model: validModel, Interaction: nilFrontend, Frontend: validFrontend, Store: &fakeStore{}, Manager: &fakeStore{}, Query: &fakeStore{}},
		{Agent: validAgent, History: validAgent, Model: validModel, Interaction: validFrontend, Frontend: nilFrontend, Store: &fakeStore{}, Manager: &fakeStore{}, Query: &fakeStore{}},
		{Agent: validAgent, History: validAgent, Model: validModel, Interaction: validFrontend, Frontend: validFrontend, Store: nilStore, Manager: &fakeStore{}, Query: &fakeStore{}},
		{Agent: validAgent, History: validAgent, Model: validModel, Interaction: validFrontend, Frontend: validFrontend, Store: &fakeStore{}, Manager: nilStore, Query: &fakeStore{}},
		{Agent: validAgent, History: validAgent, Model: validModel, Interaction: validFrontend, Frontend: validFrontend, Store: &fakeStore{}, Manager: &fakeStore{}, Query: nilStore},
	}
	process := &fakeProcess{}
	for index, deps := range tests {
		deps.Invocation = process
		deps.Lifecycle = process
		if _, _, err := New(context.Background(), appcli.Config{}, deps); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("case %d New() error=%v", index, err)
		}
	}
	validStore := &fakeStore{}
	valid := Dependencies{Agent: validAgent, History: validAgent, Model: validModel, Interaction: validFrontend, Frontend: validFrontend, Store: validStore, Manager: validStore, Query: validStore, Invocation: process, Lifecycle: process}
	missingStore := &fakeStore{}
	missingHosts := Dependencies{Agent: validAgent, History: validAgent, Model: validModel, Interaction: validFrontend, Frontend: validFrontend, Store: missingStore, Manager: missingStore, Query: missingStore}
	if _, _, err := New(context.Background(), appcli.Config{}, missingHosts); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing host dependency error=%v", err)
	}
	if _, _, err := New(context.Background(), appcli.Config{}, valid); err != nil {
		t.Fatalf("valid dependencies error=%v", err)
	}
}

func TestCheckModeValidatesGraphWithoutStartingLoop(t *testing.T) {
	process := &fakeProcess{mode: invocation.ModeCheck}
	frontend := &fakeFrontend{block: true}
	runtime := &fakeAgent{}
	_, cleanup, err := New(context.Background(), appcli.Config{}, testDependencies(frontend, runtime, &fakeStore{}, process))
	if err != nil || cleanup != nil {
		t.Fatalf("cleanup=%v err=%v", cleanup, err)
	}
}

func TestFatalLoopErrorRequestsProcessShutdownAndCleanupReturnsIt(t *testing.T) {
	wantErr := errors.New("terminal failed")
	process := &fakeProcess{done: make(chan struct{})}
	frontend := &fakeFrontend{readErr: wantErr}
	runtime := &fakeAgent{}
	_, cleanup, err := New(context.Background(), appcli.Config{}, testDependencies(frontend, runtime, &fakeStore{}, process))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.done:
	case <-time.After(time.Second):
		t.Fatal("process shutdown was not requested")
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := cleanup(cleanupCtx); !errors.Is(err, wantErr) {
		t.Fatalf("cleanup error=%v", err)
	}
	if !errors.Is(process.err, wantErr) {
		t.Fatalf("shutdown error=%v", process.err)
	}
}

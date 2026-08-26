package appcomponent

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	appcli "github.com/ingot-agent/app-cli"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/session"
)

type fakeChannel struct {
	mu        sync.Mutex
	lines     []string
	events    []interaction.Event
	block     bool
	readErr   error
	renderErr error
}

func (f *fakeChannel) Ask(context.Context, interaction.AskRequest) (interaction.AskResponse, error) {
	return interaction.AskResponse{}, errors.New("unused")
}
func (f *fakeChannel) ReadLine(ctx context.Context, _ string) (string, error) {
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
func (f *fakeChannel) Render(_ context.Context, event interaction.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.renderErr != nil {
		return f.renderErr
	}
	f.events = append(f.events, event)
	return nil
}

var _ appcli.LineInput = (*fakeChannel)(nil)

type fakeAgent struct {
	turns []agent.Turn
	err   error
}

func (f *fakeAgent) Run(_ context.Context, turn agent.Turn) (agent.Result, error) {
	f.turns = append(f.turns, turn)
	return agent.Result{Output: "ignored"}, f.err
}

type fakeStore struct {
	created   []session.Metadata
	createID  session.ID
	createErr error
	loaded    []session.ID
	loadErr   error
	summaries []session.Summary
	listErr   error
}

func (f *fakeStore) Create(_ context.Context, metadata session.Metadata) (session.ID, error) {
	f.created = append(f.created, metadata)
	if f.createErr != nil {
		return "", f.createErr
	}
	if f.createID != "" {
		return f.createID, nil
	}
	return "s1", nil
}
func (*fakeStore) Append(context.Context, session.ID, session.Entry) error { return nil }
func (f *fakeStore) Load(_ context.Context, id session.ID) ([]session.Entry, error) {
	f.loaded = append(f.loaded, id)
	return nil, f.loadErr
}
func (f *fakeStore) List(context.Context, session.Query) ([]session.Summary, error) {
	return f.summaries, f.listErr
}

func TestLoopCreatesSessionRunsAgentAndExitIsLocal(t *testing.T) {
	channel := &fakeChannel{lines: []string{"hello", "/exit"}}
	agentRuntime := &fakeAgent{}
	store := &fakeStore{}
	instance := &application{
		agent: agentRuntime, interaction: channel, input: channel, store: store, inputPrompt: "> ",
		initialTitle: "Initial", now: func() time.Time { return time.Unix(10, 0) },
	}
	if err := instance.loop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.created) != 1 || store.created[0].Title != "Initial" || len(agentRuntime.turns) != 1 || agentRuntime.turns[0].SessionID != "s1" {
		t.Fatalf("created=%#v turns=%#v", store.created, agentRuntime.turns)
	}
}

func TestNewReturnsPromptlyAndCleanupCancelsOnlyOwnedLoop(t *testing.T) {
	parent, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()
	channel := &fakeChannel{block: true}
	start := time.Now()
	_, cleanup, err := New(parent, appcli.Config{}, Dependencies{Agent: &fakeAgent{}, Interaction: channel, Input: channel, Store: &fakeStore{}})
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
	channel := &fakeChannel{lines: []string{"/new Work", "/list", "/use s2", "/help", "/exit"}}
	store := &fakeStore{createID: "s1", summaries: []session.Summary{{ID: "s1", Title: "Work"}, {ID: "s2", Title: "Other"}}}
	instance := &application{
		agent: &fakeAgent{}, interaction: channel, input: channel, store: store, inputPrompt: "> ", now: func() time.Time { return time.Unix(10, 0) },
	}
	if err := instance.loop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.created) != 1 || store.created[0].Title != "Work" || len(store.loaded) != 1 || store.loaded[0] != "s2" || instance.current != "s2" {
		t.Fatalf("created=%#v loaded=%#v current=%q", store.created, store.loaded, instance.current)
	}
	want := []string{
		"using session s1",
		"* s1  Work",
		"  s2  Other",
		"using session s2",
		"/new [title]  /use <id>  /list  /help  /exit",
	}
	if len(channel.events) != len(want) {
		t.Fatalf("events=%#v", channel.events)
	}
	for i, event := range channel.events {
		status, ok := event.(interaction.StatusEvent)
		if !ok || status.Text != want[i] {
			t.Fatalf("event %d=%#v want=%q", i, event, want[i])
		}
	}
}

func TestLoopRendersAgentErrorAndContinues(t *testing.T) {
	wantErr := errors.New("agent failed")
	channel := &fakeChannel{lines: []string{"hello", "/exit"}}
	agentRuntime := &fakeAgent{err: wantErr}
	instance := &application{
		agent: agentRuntime, interaction: channel, input: channel, store: &fakeStore{}, inputPrompt: "> ", now: time.Now,
	}
	if err := instance.loop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(channel.events) != 2 {
		t.Fatalf("events=%#v", channel.events)
	}
	errorEvent, ok := channel.events[1].(interaction.ErrorEvent)
	if !ok || !errors.Is(errorEvent.Err, wantErr) {
		t.Fatalf("error event=%#v", channel.events[1])
	}
}

func TestNewRejectsTypedNilDependencies(t *testing.T) {
	var nilAgent *fakeAgent
	var nilChannel *fakeChannel
	var nilStore *fakeStore
	tests := []Dependencies{
		{Agent: nilAgent, Interaction: &fakeChannel{}, Input: &fakeChannel{}, Store: &fakeStore{}},
		{Agent: &fakeAgent{}, Interaction: nilChannel, Input: &fakeChannel{}, Store: &fakeStore{}},
		{Agent: &fakeAgent{}, Interaction: &fakeChannel{}, Input: nilChannel, Store: &fakeStore{}},
		{Agent: &fakeAgent{}, Interaction: &fakeChannel{}, Input: &fakeChannel{}, Store: nilStore},
	}
	for i, deps := range tests {
		if _, _, err := New(context.Background(), appcli.Config{}, deps); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("case %d New() error=%v", i, err)
		}
	}
}

func TestCleanupReturnsFatalLoopError(t *testing.T) {
	wantErr := errors.New("terminal failed")
	channel := &fakeChannel{readErr: wantErr}
	_, cleanup, err := New(context.Background(), appcli.Config{}, Dependencies{
		Agent: &fakeAgent{}, Interaction: channel, Input: channel, Store: &fakeStore{},
	})
	if err != nil {
		t.Fatal(err)
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := cleanup(cleanupCtx); !errors.Is(err, wantErr) {
		t.Fatalf("cleanup error=%v", err)
	}
}

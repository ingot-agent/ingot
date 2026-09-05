package appcomponent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	appbackend "github.com/ingot-agent/app-webui"
	hostcomponent "github.com/ingot-agent/app-webui/host"
	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
)

type testAgent struct {
	run      func(context.Context, agent.Turn) (agent.Execution, error)
	messages []model.Message
}

func (a *testAgent) Run(ctx context.Context, turn agent.Turn) (agent.Execution, error) {
	if a.run != nil {
		return a.run(ctx, turn)
	}
	return agent.Execution{Result: &agent.Result{Output: content.FromText("answer")}, Outcome: agent.Outcome{Status: agent.OutcomeSucceeded}}, nil
}

func (a *testAgent) Load(context.Context, session.ID) ([]model.Message, error) {
	return a.messages, nil
}

type testStore struct {
	session.Store
	mu    sync.Mutex
	items []session.Metadata
}

func (s *testStore) List(_ context.Context) ([]session.Metadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]session.Metadata(nil), s.items...), nil
}

func (s *testStore) Create(_ context.Context, request session.CreateRequest) (session.Metadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := session.ID(fmt.Sprintf("session-%d", len(s.items)+1))
	now := time.Now().UTC()
	metadata := session.Metadata{ID: id, Title: request.Title, CreatedAt: now, UpdatedAt: now}
	s.items = append(s.items, metadata)
	return metadata, nil
}

func (s *testStore) Rename(_ context.Context, id session.ID, title string) (session.Metadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].ID == id {
			s.items[i].Title = title
			return s.items[i], nil
		}
	}
	return session.Metadata{}, session.ErrNotFound
}

func (s *testStore) Get(_ context.Context, id session.ID) (session.Metadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.items {
		if item.ID == id {
			return item, nil
		}
	}
	return session.Metadata{}, session.ErrNotFound
}
func (s *testStore) Archive(_ context.Context, id session.ID) (session.Metadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].ID == id {
			if s.items[i].ArchivedAt == nil {
				now := time.Now().UTC()
				s.items[i].ArchivedAt = &now
			}
			return s.items[i], nil
		}
	}
	return session.Metadata{}, session.ErrNotFound
}
func (s *testStore) Restore(_ context.Context, id session.ID) (session.Metadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].ID == id {
			s.items[i].ArchivedAt = nil
			return s.items[i], nil
		}
	}
	return session.Metadata{}, session.ErrNotFound
}
func (s *testStore) Delete(_ context.Context, id session.ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].ID == id {
			s.items = append(s.items[:i], s.items[i+1:]...)
			return nil
		}
	}
	return session.ErrNotFound
}
func (s *testStore) Fork(ctx context.Context, id session.ID, request session.ForkRequest) (session.Metadata, error) {
	source, err := s.Get(ctx, id)
	if err != nil {
		return session.Metadata{}, err
	}
	if request.Title == "" {
		request.Title = source.Title
	}
	return s.Create(ctx, session.CreateRequest{Title: request.Title})
}

func testDependencies(t *testing.T, runtime *testAgent, store *testStore) Dependencies {
	t.Helper()
	exports, _, err := hostcomponent.New(context.Background(), appbackend.Config{}, hostcomponent.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	process := &testProcess{shutdown: make(chan error, 1)}
	return Dependencies{Backend: exports.Runtime, Agent: ingotabi.Some[agent.Runtime](runtime), History: runtime, Store: store, Sessions: store, SessionQuery: store, Invocation: process, Lifecycle: process}
}

func testApplication(t *testing.T) *application {
	t.Helper()
	deps := testDependencies(t, &testAgent{}, &testStore{})
	controller, err := newAgentController(deps.Agent, deps.Streaming, deps.History)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := newSessionController(deps.Store, deps.Sessions, deps.SessionQuery)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &application{backend: deps.Backend, agent: controller, sessions: sessions}
	a.turns = newTurnRegistry(ctx, controller, deps.Backend.Events())
	a.operations, err = newOperationController(nil)
	if err != nil {
		t.Fatal(err)
	}
	a.operationInvocations = newOperationRegistry(ctx, a.operations, deps.Backend.Interactions(), deps.Backend.Events(), 128)
	a.config, err = (appbackend.Config{}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		a.turns.stop()
		a.operationInvocations.stop()
		select {
		case <-a.operationInvocations.done:
		case <-time.After(time.Second):
			t.Error("operation registry did not stop")
		}
		select {
		case <-a.turns.done:
		case <-time.After(time.Second):
			t.Error("turn registry did not stop")
		}
	})
	return a
}

func TestHTTPRejectsInvalidJSONObjects(t *testing.T) {
	a := testApplication(t)
	for _, body := range []string{`null`, `[]`, `{"unknown":true}`, `{} {}`, `{`, strings.Repeat(" ", maxJSONBody) + `{}`} {
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"invalid_json"`) {
			t.Fatalf("invalid JSON response = %d %s", w.Code, w.Body.String())
		}
	}
	if a.backend.Events().Cursor() != 0 {
		t.Fatal("invalid body caused a session mutation")
	}
}

func TestHTTPEventCursorValidation(t *testing.T) {
	a := testApplication(t)
	for _, test := range []struct {
		query, header, code string
		status              int
	}{
		{"", "", "event_cursor_required", http.StatusBadRequest},
		{"?after=", "0", "invalid_event_cursor", http.StatusBadRequest},
		{"?after=-1", "0", "invalid_event_cursor", http.StatusBadRequest},
		{"?after=1", "0", "event_cursor_ahead", http.StatusConflict},
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/events"+test.query, nil)
		r.Header.Set("Last-Event-ID", test.header)
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, r)
		if w.Code != test.status || !strings.Contains(w.Body.String(), test.code) || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("cursor %q / %q = %d %s", test.query, test.header, w.Code, w.Body.String())
		}
	}
}

func TestSSEFlushesHeadersBeforeFirstEvent(t *testing.T) {
	a := testApplication(t)
	server := httptest.NewServer(a.routes())
	defer server.Close()
	client := &http.Client{Timeout: time.Second}
	for _, query := range []string{"?after=0", ""} {
		r, err := http.NewRequest(http.MethodGet, server.URL+"/api/events"+query, nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Last-Event-ID", "0")
		response, err := client.Do(r)
		if err != nil {
			t.Fatalf("SSE headers were not flushed: %v", err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
			t.Fatalf("SSE response = %d, %v", response.StatusCode, response.Header)
		}
	}
}

func TestWriteJSONReportsEncodingFailure(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, http.StatusOK, json.RawMessage(`invalid`))
	if w.Code != http.StatusInternalServerError || !json.Valid(w.Body.Bytes()) {
		t.Fatalf("encoding failure response = %d %s", w.Code, w.Body.String())
	}
}

func TestSessionControllerListsAndFindsBeyondFirstPage(t *testing.T) {
	store := &testStore{items: make([]session.Metadata, 1001)}
	for i := range store.items {
		store.items[i] = session.Metadata{ID: session.ID(fmt.Sprintf("session-%d", i))}
	}
	controller, err := newSessionController(store, store, store)
	if err != nil {
		t.Fatal(err)
	}
	items, err := controller.List(context.Background())
	if err != nil || len(items) != 1001 {
		t.Fatalf("List = %d items, %v", len(items), err)
	}
	item, err := controller.Rename(context.Background(), "session-1000", "renamed")
	if err != nil || item.Title != "renamed" {
		t.Fatalf("Rename = %#v, %v", item, err)
	}
	if _, err := controller.Get(context.Background(), "missing"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("Get(missing) = %v", err)
	}
}

func TestSessionHTTPAndBootstrap(t *testing.T) {
	a := testApplication(t)
	handler := a.routes()
	for _, test := range []struct {
		method, path, body string
		status             int
	}{
		{http.MethodPost, "/api/sessions", `{"title":"first"}`, http.StatusCreated},
		{http.MethodPatch, "/api/sessions/session-1", `{"title":"renamed"}`, http.StatusOK},
		{http.MethodGet, "/api/sessions/session-1", "", http.StatusOK},
		{http.MethodGet, "/api/sessions/session-1/history", "", http.StatusOK},
		{http.MethodGet, "/api/sessions/missing", "", http.StatusNotFound},
		{http.MethodPost, "/api/turns", `{"sessionId":"session-1","attachments":[{"kind":"text","assetId":"one"}]}`, http.StatusBadRequest},
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(test.method, test.path, strings.NewReader(test.body)))
		if w.Code != test.status || !json.Valid(w.Body.Bytes()) {
			t.Fatalf("%s %s = %d %s", test.method, test.path, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	var state appbackend.StateSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if w.Header().Get("Cache-Control") != "no-store" || state.Cursor != 2 || len(state.Sessions) != 1 || state.Sessions[0].Title != "renamed" {
		t.Fatalf("state = %#v, headers = %v", state, w.Header())
	}
	if !state.Agent.Capabilities.Run || state.Agent.Capabilities.Stream || state.Turns == nil || state.Interactions == nil || state.InteractionStates == nil {
		t.Fatalf("unexpected bootstrap capabilities/collections: %#v", state)
	}
}

type snapshotSessions struct {
	sessionController
	beforeList func()
}

func (s *snapshotSessions) List(ctx context.Context) ([]appbackend.Session, error) {
	s.beforeList()
	return s.sessionController.List(ctx)
}

func TestBootstrapCapturesCursorBeforeBuildingSnapshot(t *testing.T) {
	a := testApplication(t)
	a.sessions = &snapshotSessions{sessionController: a.sessions, beforeList: func() {
		if err := a.backend.Events().Publish(appbackend.Event{Type: "session.created"}); err != nil {
			t.Fatal(err)
		}
	}}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	var state appbackend.StateSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Cursor != 0 || a.backend.Events().Cursor() != 1 {
		t.Fatalf("snapshot cursor = %d, hub cursor = %d", state.Cursor, a.backend.Events().Cursor())
	}
	sub, err := a.backend.Events().Subscribe(state.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if len(sub.Replay()) != 1 {
		t.Fatal("bootstrap lost the event produced while building its snapshot")
	}
}

type gatedSessionHub struct {
	appbackend.EventHub
	entered chan struct{}
	release chan struct{}
}

func (h *gatedSessionHub) Publish(event appbackend.Event) error {
	if item, ok := event.Data.(appbackend.Session); ok && item.Title == "first rename" {
		close(h.entered)
		<-h.release
	}
	return h.EventHub.Publish(event)
}

type runtimeWithEvents struct {
	appbackend.Runtime
	events appbackend.EventHub
}

func (r runtimeWithEvents) Events() appbackend.EventHub { return r.events }

func TestConcurrentRenamesKeepEventsInMutationOrder(t *testing.T) {
	a := testApplication(t)
	if _, err := a.sessions.Create(context.Background(), "original"); err != nil {
		t.Fatal(err)
	}
	hub := &gatedSessionHub{EventHub: a.backend.Events(), entered: make(chan struct{}), release: make(chan struct{})}
	a.backend = runtimeWithEvents{Runtime: a.backend, events: hub}
	handler := a.routes()
	rename := func(title string, done chan int) {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPatch, "/api/sessions/session-1", strings.NewReader(`{"title":"`+title+`"}`)))
		done <- w.Code
	}
	first, second := make(chan int, 1), make(chan int, 1)
	go rename("first rename", first)
	<-hub.entered
	go rename("second rename", second)
	select {
	case status := <-second:
		second <- status
	case <-time.After(20 * time.Millisecond):
	}
	close(hub.release)
	if status := <-first; status != http.StatusOK {
		t.Fatalf("first rename = %d", status)
	}
	if status := <-second; status != http.StatusOK {
		t.Fatalf("second rename = %d", status)
	}
	sub, err := hub.Subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	records := sub.Replay()
	if len(records) != 2 || !strings.Contains(string(records[0].Data), "first rename") || !strings.Contains(string(records[1].Data), "second rename") {
		t.Fatalf("rename events = %v", records)
	}
}

func readResponse(t *testing.T, response *http.Response, status int) []byte {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("HTTP response = %d %s, want %d", response.StatusCode, body, status)
	}
	return body
}

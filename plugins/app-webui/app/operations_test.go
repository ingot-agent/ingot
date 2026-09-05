package appcomponent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appbackend "github.com/ingot-agent/app-webui"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

type testOperation struct {
	definition operation.Definition
	invoke     func(context.Context, operation.Request) (operation.Result, error)
}

func (o *testOperation) Definition() operation.Definition { return o.definition }
func (o *testOperation) Invoke(ctx context.Context, request operation.Request) (operation.Result, error) {
	if o.invoke != nil {
		return o.invoke(ctx, request)
	}
	return operation.Result{Output: append(json.RawMessage(nil), request.Input...)}, nil
}

func operationFixture(name string) *testOperation {
	return &testOperation{definition: operation.Definition{Name: name, Description: "Echo a value",
		InputSchema:  json.RawMessage(`{"type":"object","required":["value"],"properties":{"value":{"type":"integer","minimum":9007199254740993}},"additionalProperties":false}`),
		OutputSchema: json.RawMessage(`{"type":"object","required":["value"],"properties":{"value":{"type":"integer"}},"additionalProperties":false}`)}}
}

func configureOperations(t *testing.T, a *application, retention int, operations ...operation.Operation) {
	t.Helper()
	controller, err := newOperationController(operations)
	if err != nil {
		t.Fatal(err)
	}
	a.operationInvocations.stop()
	a.operations = controller
	a.operationInvocations = newOperationRegistry(a.turns.appCtx, controller, a.backend.Interactions(), a.backend.Events(), retention)
}

func TestOperationDefinitionsValidateAndOwnSnapshots(t *testing.T) {
	first, second := operationFixture("z.echo"), operationFixture("a.echo")
	c, err := newOperationController([]operation.Operation{first, second})
	if err != nil {
		t.Fatal(err)
	}
	definitions := c.List()
	if len(definitions) != 2 || definitions[0].Name != "z.echo" || definitions[1].Name != "a.echo" {
		t.Fatalf("definition order = %v", definitions)
	}
	definitions[0].InputSchema[0] = '!'
	first.definition.InputSchema[0] = '!'
	if !json.Valid(c.List()[0].InputSchema) {
		t.Fatal("definitions retained caller-owned bytes")
	}
	for _, test := range []struct {
		name      string
		alter     func(*testOperation)
		duplicate bool
	}{
		{"name", func(o *testOperation) { o.definition.Name = "Bad/Name" }, false},
		{"description", func(o *testOperation) { o.definition.Description = "" }, false},
		{"schema", func(o *testOperation) { o.definition.InputSchema = json.RawMessage(`{"type":"bogus"}`) }, false},
		{"draft", func(o *testOperation) {
			o.definition.InputSchema = json.RawMessage(`{"$schema":"http://json-schema.org/draft-07/schema#","type":"object"}`)
		}, false},
		{"external ref", func(o *testOperation) {
			o.definition.InputSchema = json.RawMessage(`{"$ref":"file:///should-not-be-read.json"}`)
		}, false},
		{"output schema", func(o *testOperation) { o.definition.OutputSchema = json.RawMessage(`null`) }, false},
		{"duplicate", func(*testOperation) {}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := operationFixture("echo")
			test.alter(o)
			operations := []operation.Operation{o}
			if test.duplicate {
				operations = append(operations, o)
			}
			if _, err := newOperationController(operations); !errors.Is(err, appbackend.ErrInvalidOperationDefinition) {
				t.Fatalf("invalid definition = %v", err)
			}
		})
	}
}

func TestOperationInputValidationPrecedesDispatchAndChecksOutput(t *testing.T) {
	o := operationFixture("echo")
	var calls atomic.Int32
	o.invoke = func(_ context.Context, request operation.Request) (operation.Result, error) {
		calls.Add(1)
		return operation.Result{Output: request.Input}, nil
	}
	c, err := newOperationController([]operation.Operation{o})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{`null`, `[]`, `{}`, `{"value":9007199254740992}`, `{"value":"9007199254740993"}`, `{"value":9007199254740993} {}`} {
		_, err := c.Invoke(context.Background(), "echo", operation.Request{Input: json.RawMessage(input), Interaction: interaction.Unavailable()})
		if !errors.Is(err, appbackend.ErrInvalidOperationInput) {
			t.Fatalf("input %s: %v", input, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input dispatched an operation")
	}
	input := json.RawMessage(`{"value":9007199254740993}`)
	result, err := c.Invoke(context.Background(), "echo", operation.Request{Input: input, Interaction: interaction.Unavailable()})
	if err != nil || calls.Load() != 1 {
		t.Fatalf("valid large integer: %v, calls=%d", err, calls.Load())
	}
	input[0] = '!'
	if !json.Valid(result.Output) {
		t.Fatal("result retained request bytes")
	}
	o.invoke = func(context.Context, operation.Request) (operation.Result, error) {
		return operation.Result{Output: json.RawMessage(`{"value":"wrong"}`)}, nil
	}
	_, err = c.Invoke(context.Background(), "echo", operation.Request{Input: json.RawMessage(`{"value":9007199254740993}`), Interaction: interaction.Unavailable()})
	if !errors.Is(err, appbackend.ErrInvalidOperationOutput) {
		t.Fatalf("invalid output: %v", err)
	}
}

func TestOperationSchemaSupportsLocalReferences(t *testing.T) {
	o := operationFixture("echo")
	o.definition.InputSchema = json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema#","$defs":{"request":{"type":["object"],"required":["value"]}},"$ref":"#/$defs/request"}`)
	c, err := newOperationController([]operation.Operation{o})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.validateInput("echo", json.RawMessage(`{"value":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := c.validateInput("echo", json.RawMessage(`{}`)); !errors.Is(err, appbackend.ErrInvalidOperationInput) {
		t.Fatalf("local ref was ignored: %v", err)
	}
}

func waitOperation(t *testing.T, a *application, id string, terminal bool) appbackend.OperationSnapshot {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		for _, snapshot := range a.operationInvocations.Snapshots() {
			if snapshot.ID == id && (!terminal || snapshot.Status != "running") {
				return snapshot
			}
		}
		select {
		case <-deadline.C:
			t.Fatal("timed out waiting for operation", id)
		case <-ticker.C:
		}
	}
}

func TestOperationHTTPInteractionAndRefreshRetention(t *testing.T) {
	a := testApplication(t)
	o := operationFixture("confirm")
	observedSession := make(chan string, 1)
	o.invoke = func(ctx context.Context, request operation.Request) (operation.Result, error) {
		observedSession <- string(request.SessionID)
		_, err := request.Interaction.Request(ctx, interaction.Request{Name: "approve", Fields: []interaction.Field{{Name: "yes", Kind: interaction.FieldBoolean, Required: true}}})
		if err != nil {
			return operation.Result{}, err
		}
		return operation.Result{Output: json.RawMessage(`{"value":42}`)}, nil
	}
	configureOperations(t, a, 2, o)
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodPost, "/api/operations/confirm", strings.NewReader(`{"sessionId":"session-1","input":{"value":9007199254740993}}`)).WithContext(ctx)
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	cancel()
	if w.Code != http.StatusAccepted {
		t.Fatalf("invoke = %d %s", w.Code, w.Body.String())
	}
	var accepted struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if got := <-observedSession; got != "session-1" {
		t.Fatalf("session scope = %q", got)
	}
	var pending appbackend.PendingInteraction
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		items := a.backend.Interactions().Pending()
		if len(items) == 1 {
			pending = items[0]
			break
		}
		time.Sleep(time.Millisecond)
	}
	if pending.ID == "" || pending.Scope == nil || pending.Scope.Operation.InvocationID != accepted.ID {
		t.Fatalf("pending scope = %#v", pending)
	}
	if snapshot := waitOperation(t, a, accepted.ID, false); snapshot.Status != "running" {
		t.Fatal("HTTP disconnect canceled the operation")
	}
	w = httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/interactions/"+pending.ID+"/response", strings.NewReader(`{"values":{"yes":true}}`)))
	if w.Code != http.StatusNoContent {
		t.Fatalf("response = %d %s", w.Code, w.Body.String())
	}
	snapshot := waitOperation(t, a, accepted.ID, true)
	if snapshot.Status != "succeeded" || string(snapshot.Result.Output) != `{"value":42}` {
		t.Fatalf("settlement = %#v", snapshot)
	}
	snapshot.Result.Output[0] = '!'
	w = httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	var state appbackend.StateSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.OperationInvocations) != 1 || string(state.OperationInvocations[0].Result.Output) != `{"value":42}` || len(state.Interactions) != 0 {
		t.Fatalf("refresh state = %#v", state)
	}
	w = httptest.NewRecorder()
	a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/operation-invocations/"+accepted.ID, nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("cancel terminal operation = %d", w.Code)
	}
}

func TestOperationRetentionCancellationAndPreflightErrors(t *testing.T) {
	a := testApplication(t)
	o := operationFixture("echo")
	configureOperations(t, a, 1, o)
	for _, test := range []struct {
		path, body string
		status     int
	}{
		{"/api/operations/missing", `{"input":{}}`, http.StatusNotFound},
		{"/api/operations/echo", `{"input":{}}`, http.StatusBadRequest},
	} {
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body)))
		if w.Code != test.status {
			t.Fatalf("preflight = %d %s", w.Code, w.Body.String())
		}
	}
	if len(a.operationInvocations.Snapshots()) != 0 {
		t.Fatal("invalid input created an invocation")
	}
	first, err := a.operationInvocations.Start("echo", "", json.RawMessage(`{"value":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	waitOperation(t, a, first, true)
	second, err := a.operationInvocations.Start("echo", "", json.RawMessage(`{"value":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	waitOperation(t, a, second, true)
	if snapshots := a.operationInvocations.Snapshots(); len(snapshots) != 1 || snapshots[0].ID != second {
		t.Fatalf("retention = %v", snapshots)
	}
	if err := a.operationInvocations.Cancel(first); !errors.Is(err, appbackend.ErrOperationInvocationNotFound) {
		t.Fatalf("evicted invocation = %v", err)
	}
	o.invoke = func(ctx context.Context, _ operation.Request) (operation.Result, error) {
		<-ctx.Done()
		return operation.Result{}, ctx.Err()
	}
	third, err := a.operationInvocations.Start("echo", "", json.RawMessage(`{"value":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.operationInvocations.Cancel(third); err != nil {
		t.Fatal(err)
	}
	if snapshot := waitOperation(t, a, third, true); snapshot.Status != "canceled" || snapshot.Error.Code != "canceled" {
		t.Fatalf("cancellation = %#v", snapshot)
	}
}

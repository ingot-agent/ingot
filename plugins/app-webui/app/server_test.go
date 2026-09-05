package appcomponent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	appbackend "github.com/ingot-agent/app-webui"
	"github.com/ingot-agent/ingot-abi/invocation"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/operation"
)

type testProcess struct {
	check    bool
	shutdown chan error
}

func (*testProcess) Arguments() []string { return nil }
func (p *testProcess) Mode() invocation.Mode {
	if p.check {
		return invocation.ModeCheck
	}
	return invocation.ModeRun
}
func (p *testProcess) RequestShutdown(err error) {
	select {
	case p.shutdown <- err:
	default:
	}
}

func TestCheckModeDoesNotOpenListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx := context.Background()
	cfg := appbackend.Config{Backend: appbackend.BackendConfig{Address: listener.Addr().String()}}
	deps := testDependencies(t, &testAgent{}, &testStore{})
	deps.Invocation = &testProcess{check: true}
	_, cleanup, err := New(ctx, cfg, deps)
	if err != nil || cleanup != nil {
		t.Fatalf("check mode started server or failed on occupied port: cleanup = %v, err = %v", cleanup != nil, err)
	}
}

func TestConstructorRequiresProcessControl(t *testing.T) {
	deps := testDependencies(t, &testAgent{}, &testStore{})
	deps.Lifecycle = nil
	_, cleanup, err := New(context.Background(), appbackend.Config{}, deps)
	if cleanup != nil {
		_ = cleanup(context.Background())
	}
	if !errors.Is(err, appbackend.ErrInvalidConfig) {
		t.Fatalf("missing process control = %v", err)
	}
}

func TestServerFailureRequestsProcessShutdown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	process := &testProcess{shutdown: make(chan error, 1)}
	a := &application{server: &http.Server{}, listener: listener, serveDone: make(chan struct{})}
	go a.serve(process)
	select {
	case err := <-process.shutdown:
		if err == nil {
			t.Fatal("unexpected server failure was suppressed")
		}
	case <-time.After(time.Second):
		t.Fatal("server failure did not request process shutdown")
	}
	<-a.serveDone
}

func TestCleanupCancelsSSEAndWaitsForBackgroundTurn(t *testing.T) {
	started := make(chan context.Context, 1)
	canceled := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	operationRelease := make(chan struct{})
	operationStarted, operationCanceled := make(chan struct{}), make(chan struct{})
	var operationReleaseOnce sync.Once
	unblockOperation := func() { operationReleaseOnce.Do(func() { close(operationRelease) }) }
	defer unblockOperation()
	runtime := &testAgent{run: func(ctx context.Context, turn agent.Turn) (agent.Execution, error) {
		started <- ctx
		<-ctx.Done()
		close(canceled)
		<-release
		return agent.Execution{}, ctx.Err()
	}}
	deps := testDependencies(t, runtime, &testStore{})
	op := operationFixture("wait")
	op.invoke = func(ctx context.Context, _ operation.Request) (operation.Result, error) {
		close(operationStarted)
		<-ctx.Done()
		close(operationCanceled)
		<-operationRelease
		return operation.Result{}, ctx.Err()
	}
	deps.Operations = []operation.Operation{op}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	ctx := context.Background()
	_, cleanup, err := New(ctx, appbackend.Config{Backend: appbackend.BackendConfig{Address: address}}, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		unblock()
		unblockOperation()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := cleanup(cleanupCtx); err != nil {
			t.Errorf("repeated cleanup: %v", err)
		}
	})
	client := &http.Client{Timeout: 3 * time.Second}
	operationResponse, err := client.Post("http://"+address+"/api/operations/wait", "application/json", strings.NewReader(`{"input":{"value":9007199254740993}}`))
	if err != nil {
		t.Fatal(err)
	}
	readResponse(t, operationResponse, http.StatusAccepted)
	select {
	case <-operationStarted:
	case <-time.After(time.Second):
		t.Fatal("operation did not start")
	}
	requestCtx, disconnect := context.WithCancel(context.Background())
	defer disconnect()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, "http://"+address+"/api/turns", strings.NewReader(`{"sessionId":"session-1","input":""}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var invocation struct{ ID string }
	if err := json.Unmarshal(readResponse(t, response, http.StatusAccepted), &invocation); err != nil {
		t.Fatal(err)
	}
	var turnCtx context.Context
	select {
	case turnCtx = <-started:
	case <-time.After(time.Second):
		t.Fatal("turn did not start")
	}
	disconnect()
	select {
	case <-turnCtx.Done():
		t.Fatal("HTTP completion canceled the turn")
	case <-time.After(20 * time.Millisecond):
	}
	response, err = client.Get("http://" + address + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	var state appbackend.StateSnapshot
	if err := json.Unmarshal(readResponse(t, response, http.StatusOK), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Turns) != 1 || state.Turns[0].ID != invocation.ID {
		t.Fatalf("refresh lost running turn: %#v", state.Turns)
	}
	response, err = client.Get("http://" + address + "/api/events?after=0")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	streamDone := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, response.Body); streamDone <- err }()
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cleanupDone := make(chan error, 1)
	go func() { cleanupDone <- cleanup(cleanupCtx) }()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not cancel running turn")
	}
	select {
	case <-operationCanceled:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not cancel operation")
	}
	select {
	case err := <-cleanupDone:
		t.Fatalf("cleanup returned before the turn unwound: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	select {
	case err := <-streamDone:
		if err != nil {
			t.Fatalf("SSE did not close cleanly: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown left the SSE connection open")
	}
	unblock()
	select {
	case err := <-cleanupDone:
		t.Fatalf("cleanup returned before operation unwound: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	unblockOperation()
	if err := <-cleanupDone; err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}

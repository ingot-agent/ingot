package appcomponent

// This opt-in fixture serves the real HTTP/SSE boundary with deterministic SDK
// adapters. It is used by browser regression tests without credentials or model
// network calls, and is excluded from the distributed runtime.
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	appbackend "github.com/ingot-agent/app-webui"
	hostcomponent "github.com/ingot-agent/app-webui/host"
	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/asset"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/operation"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

type browserAgent struct {
	mu       sync.Mutex
	history  map[session.ID][]model.Message
	active   map[session.ID]chan struct{}
	host     appbackend.Runtime
	observer observation.Observer
	sequence atomic.Uint64
}

func (b *browserAgent) Load(ctx context.Context, id session.ID) ([]model.Message, error) {
	b.mu.Lock()
	done := b.active[id]
	b.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]model.Message(nil), b.history[id]...), nil
}
func (b *browserAgent) Run(ctx context.Context, turn agent.Turn) (agent.Execution, error) {
	return b.Stream(ctx, turn, func(agent.StreamEvent) error { return nil })
}
func (b *browserAgent) Stream(ctx context.Context, turn agent.Turn, handler agent.StreamHandler) (result agent.Execution, err error) {
	start := time.Now()
	correlation := observation.Correlation{SessionID: turn.SessionID, TurnID: observation.ID(fmt.Sprintf("sdk-%d", b.sequence.Add(1)))}
	ctx = observation.WithCorrelation(ctx, correlation)
	seq := uint64(0)
	emit := func(detail observation.Detail) {
		seq++
		b.observer.Observe(observation.Event{Sequence: seq, Time: time.Now(), Correlation: correlation, Detail: detail})
	}
	b.mu.Lock()
	done := make(chan struct{})
	b.active[turn.SessionID] = done
	b.history[turn.SessionID] = append(b.history[turn.SessionID], model.Message{Role: model.RoleUser, Content: content.FromInput(turn.Input, turn.Attachments)})
	b.mu.Unlock()
	emit(observation.TurnStarted{Turn: turn})
	emit(observation.RoundStarted{})
	defer func() {
		status := observation.StatusSucceeded
		outcomeStatus := agent.OutcomeSucceeded
		if err != nil {
			status, outcomeStatus = observation.StatusFailed, agent.OutcomeFailed
		}
		if ctx.Err() != nil {
			status, outcomeStatus = observation.StatusCanceled, agent.OutcomeCanceled
		}
		result.Outcome = agent.Outcome{
			Status: outcomeStatus, Duration: time.Since(start),
			Accounting: agent.Accounting{Rounds: 1, ModelInvocations: 1, ToolCalls: 1, Usage: agent.TokenUsage{InputTokens: 48, OutputTokens: 24, TotalTokens: 72, Coverage: agent.UsageComplete}},
		}
		message := ""
		if err != nil {
			message = err.Error()
		}
		emit(observation.RoundFinished{Status: status, Error: message})
		emit(observation.TurnFinished{Status: status, Error: message, Outcome: result.Outcome, Result: result.Result})
		b.mu.Lock()
		delete(b.active, turn.SessionID)
		close(done)
		b.mu.Unlock()
	}()
	if strings.Contains(turn.Input, "fail") {
		return result, fmt.Errorf("fixture execution failed")
	}
	if err = handler(agent.StreamEvent{Kind: agent.StreamReasoningDelta, TextDelta: "Checking the request."}); err != nil {
		return result, err
	}
	correlation.ToolCallID = "tool-" + string(correlation.TurnID)
	call := tool.Call{ID: correlation.ToolCallID, Name: "workspace.inspect", Arguments: json.RawMessage("{\"path\":\".\"}")}
	emit(observation.ToolStarted{Call: call})
	ctx = observation.WithCorrelation(ctx, correlation)
	if strings.Contains(turn.Input, "approve") || strings.Contains(turn.Input, "ask") {
		field := interaction.Field{Name: "answer", Label: "Answer", Kind: interaction.FieldString, Required: true, Options: []interaction.Option{{Value: "yes", Label: "Yes"}, {Value: "no", Label: "No"}}}
		description := "How would you like to continue?"
		if strings.Contains(turn.Input, "approve") {
			field.Kind = interaction.FieldChoice
			description = "Allow workspace inspection?"
		}
		if _, err = b.host.Interactions().Request(ctx, interaction.Request{Name: "confirmation", Description: description, Fields: []interaction.Field{field}}); err != nil {
			return result, err
		}
	}
	emit(observation.ToolFinished{Status: observation.StatusSucceeded, Result: &tool.Result{Content: content.FromText("Workspace is ready.")}})
	correlation.ToolCallID = ""
	if strings.Contains(turn.Input, "hold") {
		if err = handler(agent.StreamEvent{Kind: agent.StreamOutputDelta, TextDelta: "Partial response"}); err != nil {
			return result, err
		}
		<-ctx.Done()
		return result, ctx.Err()
	}
	output := "Your workspace is ready. We can take the next step together."
	if len(turn.Attachments) > 0 {
		output = "Attachment received."
	}
	for _, chunk := range strings.SplitAfter(output, " ") {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
		if err = handler(agent.StreamEvent{Kind: agent.StreamOutputDelta, TextDelta: chunk}); err != nil {
			return result, err
		}
	}
	result.Result = &agent.Result{Output: content.FromText(output)}
	b.mu.Lock()
	b.history[turn.SessionID] = append(b.history[turn.SessionID], model.Message{Role: model.RoleAssistant, Content: result.Result.Output})
	b.mu.Unlock()
	return result, nil
}

type browserAssets struct {
	mu    sync.Mutex
	items map[string][]byte
}

func (s *browserAssets) Put(_ context.Context, request asset.PutRequest) (asset.Reference, asset.Info, error) {
	data, err := io.ReadAll(request.Body)
	if err != nil {
		return asset.Reference{}, asset.Info{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := fmt.Sprintf("asset-%d", len(s.items)+1)
	s.items[id] = data
	return asset.Reference{ID: id}, asset.Info{Size: uint64(len(data))}, nil
}
func (s *browserAssets) Stat(_ context.Context, reference asset.Reference) (asset.Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.items[reference.ID]
	if !ok {
		return asset.Info{}, fs.ErrNotExist
	}
	return asset.Info{Size: uint64(len(data))}, nil
}
func (s *browserAssets) Open(_ context.Context, reference asset.Reference) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.items[reference.ID]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
func TestBrowserFixture(t *testing.T) {
	address := os.Getenv("INGOT_WEBUI_FIXTURE_ADDR")
	if address == "" {
		t.Skip("opt-in browser fixture")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	a := testApplication(t)
	a.config.MaxAssetBytes = 64 << 20
	a.config.Heartbeat = time.Second
	// Construct host once so request scope and observation share the same hub.
	host, _, err := hostcomponent.New(ctx, appbackend.Config{}, hostcomponent.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	a.backend = host.Runtime
	b := &browserAgent{history: make(map[session.ID][]model.Message), active: make(map[session.ID]chan struct{}), host: host.Runtime, observer: host.Observer}
	streaming := ingotabi.Some[agent.StreamingRuntime](b)
	if os.Getenv("INGOT_WEBUI_FIXTURE_RUN_ONLY") == "1" {
		streaming = ingotabi.None[agent.StreamingRuntime]()
	}
	a.agent, err = newAgentController(ingotabi.Some[agent.Runtime](b), streaming, b)
	if err != nil {
		t.Fatal(err)
	}
	a.turns = newTurnRegistry(ctx, a.agent, host.Runtime.Events())
	a.assets = &browserAssets{items: make(map[string][]byte)}
	echo := operationFixture("echo")
	echo.definition.InputSchema = json.RawMessage("{\"type\":\"object\",\"properties\":{\"value\":{\"type\":\"integer\"}},\"required\":[\"value\"]}")
	echo.invoke = func(_ context.Context, request operation.Request) (operation.Result, error) {
		return operation.Result{Output: request.Input}, nil
	}
	wait := operationFixture("wait")
	wait.definition.InputSchema = echo.definition.InputSchema
	wait.invoke = func(ctx context.Context, _ operation.Request) (operation.Result, error) {
		<-ctx.Done()
		return operation.Result{}, ctx.Err()
	}
	complex := operationFixture("complex")
	complex.definition.InputSchema = json.RawMessage("{\"type\":\"object\",\"properties\":{\"items\":{\"type\":\"array\"}},\"required\":[\"items\"]}")
	complex.definition.OutputSchema = json.RawMessage("{\"type\":\"object\"}")
	complex.invoke = echo.invoke
	a.operations, err = newOperationController([]operation.Operation{echo, wait, complex})
	if err != nil {
		t.Fatal(err)
	}
	a.operationInvocations = newOperationRegistry(ctx, a.operations, a.backend.Interactions(), a.backend.Events(), 128)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: a.routes(), ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	t.Logf("Browser fixture: http://%s", address)
	<-ctx.Done()
}

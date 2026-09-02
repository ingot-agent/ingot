// Package observationcomponent implements the observation ingestion and
// delivery component of the agent.default composite plugin.
package observationcomponent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	agentdefault "github.com/ingot-agent/agent-default"
	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/tool"
)

const defaultPendingProgressLimit = 1024

// Dependencies contains every independently composed execution Observer.
type Dependencies struct {
	Observers []observation.Observer
}

// Exports contains the single producer-facing observation Consumer.
type Exports struct {
	Consumer observation.Consumer
}

type discardConsumer struct{}

func (discardConsumer) Emit(context.Context, observation.Detail) {}

type hub struct {
	mu              sync.Mutex
	ready           *sync.Cond
	observers       []observation.Observer
	queue           []observation.Event
	sequences       map[observation.ID]uint64
	pendingProgress int
	progressLimit   int
	accepting       bool
	done            chan struct{}
	panics          atomic.Uint64
}

// New snapshots the Observer collection and starts ordered asynchronous
// delivery. A plugin without observers receives a zero-cost discard Consumer.
func New(ctx context.Context, _ agentdefault.Config, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, fmt.Errorf("construct agent.default observation: nil context")
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	observers := make([]observation.Observer, len(deps.Observers))
	for i, observer := range deps.Observers {
		if isNil(observer) {
			return Exports{}, nil, fmt.Errorf("construct agent.default observation: observers[%d] is nil", i)
		}
		observers[i] = observer
	}
	if len(observers) == 0 {
		return Exports{Consumer: discardConsumer{}}, nil, nil
	}
	instance := &hub{
		observers: observers, sequences: make(map[observation.ID]uint64),
		progressLimit: defaultPendingProgressLimit, accepting: true, done: make(chan struct{}),
	}
	instance.ready = sync.NewCond(&instance.mu)
	go instance.deliver()
	return Exports{Consumer: instance}, ingotabi.Cleanup(instance.cleanup), nil
}

func (h *hub) Emit(ctx context.Context, detail observation.Detail) {
	if detail == nil {
		return
	}
	correlation, _ := observation.CorrelationFromContext(ctx)
	snapshot := cloneDetail(detail)
	if snapshot == nil {
		return
	}
	progress := isProgress(snapshot)

	h.mu.Lock()
	if !h.accepting || progress && h.pendingProgress >= h.progressLimit {
		h.mu.Unlock()
		return
	}
	sequence := h.sequences[correlation.TurnID] + 1
	h.sequences[correlation.TurnID] = sequence
	event := observation.Event{
		Time: time.Now(), Sequence: sequence, Correlation: correlation, Detail: snapshot,
	}
	h.queue = append(h.queue, event)
	if progress {
		h.pendingProgress++
	}
	if isTurnFinished(snapshot) {
		delete(h.sequences, correlation.TurnID)
	}
	h.ready.Signal()
	h.mu.Unlock()
}

func (h *hub) deliver() {
	defer close(h.done)
	for {
		h.mu.Lock()
		for len(h.queue) == 0 && h.accepting {
			h.ready.Wait()
		}
		if len(h.queue) == 0 {
			h.mu.Unlock()
			return
		}
		event := h.queue[0]
		if len(h.queue) == 1 {
			h.queue = nil
		} else {
			h.queue[0] = observation.Event{}
			h.queue = h.queue[1:]
		}
		if isProgress(event.Detail) {
			h.pendingProgress--
		}
		h.mu.Unlock()

		for _, observer := range h.observers {
			h.observe(observer, cloneEvent(event))
		}
	}
}

func (h *hub) observe(observer observation.Observer, event observation.Event) {
	defer func() {
		if recover() != nil {
			h.panics.Add(1)
		}
	}()
	observer.Observe(event)
}

func (h *hub) cleanup(ctx context.Context) error {
	h.mu.Lock()
	if h.accepting {
		h.accepting = false
		h.ready.Broadcast()
	}
	h.mu.Unlock()
	if ctx == nil {
		return context.Canceled
	}
	select {
	case <-h.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func cloneEvent(event observation.Event) observation.Event {
	event.Detail = cloneDetail(event.Detail)
	return event
}

func cloneDetail(detail observation.Detail) observation.Detail {
	switch value := detail.(type) {
	case observation.TurnStarted:
		value.Turn = cloneTurn(value.Turn)
		return value
	case *observation.TurnStarted:
		if value == nil {
			return nil
		}
		return observation.TurnStarted{Turn: cloneTurn(value.Turn)}
	case observation.TurnFinished:
		value.Result = cloneAgentResult(value.Result)
		value.Outcome = cloneOutcome(value.Outcome)
		return value
	case *observation.TurnFinished:
		if value == nil {
			return nil
		}
		return observation.TurnFinished{
			Status: value.Status, Result: cloneAgentResult(value.Result), Outcome: cloneOutcome(value.Outcome), Error: value.Error,
		}
	case observation.RoundStarted:
		return value
	case *observation.RoundStarted:
		if value == nil {
			return nil
		}
		return observation.RoundStarted{}
	case observation.RoundFinished:
		value.Result = cloneRoundResult(value.Result)
		return value
	case *observation.RoundFinished:
		if value == nil {
			return nil
		}
		return observation.RoundFinished{Status: value.Status, Result: cloneRoundResult(value.Result), Error: value.Error}
	case observation.ModelStarted:
		value.Request = cloneRequest(value.Request)
		return value
	case *observation.ModelStarted:
		if value == nil {
			return nil
		}
		return observation.ModelStarted{Request: cloneRequest(value.Request)}
	case observation.ModelProgress:
		value.Progress = cloneStreamEvent(value.Progress)
		return value
	case *observation.ModelProgress:
		if value == nil {
			return nil
		}
		return observation.ModelProgress{Progress: cloneStreamEvent(value.Progress)}
	case observation.ModelFinished:
		value.Response = cloneResponse(value.Response)
		return value
	case *observation.ModelFinished:
		if value == nil {
			return nil
		}
		return observation.ModelFinished{Status: value.Status, Response: cloneResponse(value.Response), Error: value.Error}
	case observation.ToolStarted:
		value.Call = cloneCall(value.Call)
		return value
	case *observation.ToolStarted:
		if value == nil {
			return nil
		}
		return observation.ToolStarted{Call: cloneCall(value.Call)}
	case observation.ToolProgress:
		value.Progress.Content = content.Clone(value.Progress.Content)
		return value
	case *observation.ToolProgress:
		if value == nil {
			return nil
		}
		return observation.ToolProgress{Progress: tool.Progress{Channel: value.Progress.Channel, Content: content.Clone(value.Progress.Content)}}
	case observation.ToolFinished:
		value.Result = cloneToolResult(value.Result)
		return value
	case *observation.ToolFinished:
		if value == nil {
			return nil
		}
		return observation.ToolFinished{Status: value.Status, Result: cloneToolResult(value.Result), Error: value.Error}
	default:
		return nil
	}
}

func cloneTurn(value agent.Turn) agent.Turn {
	value.Attachments = content.CloneAttachments(value.Attachments)
	return value
}

func cloneAgentResult(value *agent.Result) *agent.Result {
	if value == nil {
		return nil
	}
	return &agent.Result{Output: content.Clone(value.Output)}
}

func cloneOutcome(value agent.Outcome) agent.Outcome {
	value.Accounting.Models = append([]agent.ModelAccounting(nil), value.Accounting.Models...)
	if value.Failure != nil {
		failure := *value.Failure
		if value.Failure.RoundIndex != nil {
			index := *value.Failure.RoundIndex
			failure.RoundIndex = &index
		}
		value.Failure = &failure
	}
	return value
}

func cloneRoundResult(value *agent.RoundResult) *agent.RoundResult {
	if value == nil {
		return nil
	}
	return &agent.RoundResult{Decision: cloneMessage(value.Decision), ToolMessages: cloneMessages(value.ToolMessages)}
}

func cloneRequest(value model.Request) model.Request {
	value.Messages = cloneMessages(value.Messages)
	definitions := value.Tools
	value.Tools = make([]tool.Definition, len(definitions))
	for i, definition := range definitions {
		value.Tools[i] = definition
		value.Tools[i].InputSchema = append(json.RawMessage(nil), definition.InputSchema...)
	}
	value.Temperature = cloneFloat(value.Temperature)
	value.MaxTokens = cloneInt(value.MaxTokens)
	value.Stop = append([]string(nil), value.Stop...)
	return value
}

func cloneResponse(value *model.Response) *model.Response {
	if value == nil {
		return nil
	}
	result := *value
	result.Message = cloneMessage(value.Message)
	return &result
}

func cloneMessages(values []model.Message) []model.Message {
	if values == nil {
		return nil
	}
	result := make([]model.Message, len(values))
	for i, value := range values {
		result[i] = cloneMessage(value)
	}
	return result
}

func cloneMessage(value model.Message) model.Message {
	value.Content = content.Clone(value.Content)
	if value.ToolCalls == nil {
		return value
	}
	calls := value.ToolCalls
	value.ToolCalls = make([]tool.Call, len(calls))
	for i, call := range calls {
		value.ToolCalls[i] = cloneCall(call)
	}
	return value
}

func cloneStreamEvent(value model.StreamEvent) model.StreamEvent {
	value.DataDelta = append([]byte(nil), value.DataDelta...)
	return value
}

func cloneCall(value tool.Call) tool.Call {
	value.Arguments = append(json.RawMessage(nil), value.Arguments...)
	return value
}

func cloneToolResult(value *tool.Result) *tool.Result {
	if value == nil {
		return nil
	}
	return &tool.Result{Content: content.Clone(value.Content)}
}

func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func isProgress(detail observation.Detail) bool {
	switch detail.(type) {
	case observation.ModelProgress, observation.ToolProgress:
		return true
	default:
		return false
	}
}

func isTurnFinished(detail observation.Detail) bool {
	_, ok := detail.(observation.TurnFinished)
	return ok
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ observation.Consumer = (*hub)(nil)
var _ observation.Consumer = discardConsumer{}

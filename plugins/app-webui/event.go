package appbackend

import (
	"errors"
)

var (
	// ErrCapabilityUnavailable indicates an execution mode absent from the graph.
	ErrCapabilityUnavailable = errors.New("capability unavailable")
	// ErrInvalidOperationDefinition indicates a malformed or duplicate definition.
	ErrInvalidOperationDefinition = errors.New("invalid operation definition")
	// ErrOperationNotFound indicates that lookup failed before invocation.
	ErrOperationNotFound = errors.New("operation not found")
	// ErrInvalidOperationInput indicates invalid input before dispatch.
	ErrInvalidOperationInput = errors.New("invalid operation input")
	// ErrInvalidOperationOutput indicates an invalid successful plugin result.
	ErrInvalidOperationOutput = errors.New("invalid operation output")
	// ErrOperationInvocationNotFound indicates an unknown or evicted invocation.
	ErrOperationInvocationNotFound = errors.New("operation invocation not found")
	// ErrOperationSettled indicates that an invocation is already terminal.
	ErrOperationSettled = errors.New("operation invocation already settled")
	// ErrApplicationClosed indicates that shutdown has stopped new invocations.
	ErrApplicationClosed = errors.New("application is shutting down")
	// ErrCursorExpired indicates that the requested cursor is older than the
	// bounded replay window.
	ErrCursorExpired = errors.New("event cursor expired")
	// ErrCursorAhead indicates that a cursor is newer than the hub cursor.
	ErrCursorAhead = errors.New("event cursor is ahead of the hub")
	// ErrInteractionNotFound indicates that a pending interaction has already
	// been settled or never existed.
	ErrInteractionNotFound = errors.New("interaction not found or already settled")
	// ErrInvalidInteractionResponse indicates that submitted browser values do
	// not satisfy the original interaction request.
	ErrInvalidInteractionResponse = errors.New("invalid interaction response")
	// ErrTurnNotFound indicates that a running turn invocation does not exist.
	ErrTurnNotFound = errors.New("turn invocation not found")
)

// Event is the application-level envelope transported by SSE.
type Event struct {
	Type  string `json:"type"`
	Scope *Scope `json:"scope,omitempty"`
	Data  any    `json:"data,omitempty"`
}

// Scope correlates an event with an agent or operation execution.
type Scope struct {
	Agent     *AgentScope     `json:"agent,omitempty"`
	Operation *OperationScope `json:"operation,omitempty"`
}

// AgentScope identifies an agent execution position.
type AgentScope struct {
	SessionID  string `json:"sessionId,omitempty"`
	TurnID     string `json:"turnId,omitempty"`
	RoundIndex *int   `json:"roundIndex,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
}

// OperationScope identifies one operation invocation.
type OperationScope struct {
	InvocationID string `json:"invocationId"`
}

// EventRecord is an immutable serialized event with its transport cursor.
type EventRecord struct {
	ID   uint64
	Data []byte
}

// EventSink accepts application events without waiting for SSE consumers.
type EventSink interface {
	Publish(Event) error
}

// Subscription is an atomic replay-to-live event handoff.
type Subscription interface {
	Replay() []EventRecord
	Events() <-chan EventRecord
	Close()
}

// EventHub owns the process-local monotonic cursor and bounded replay window.
type EventHub interface {
	EventSink
	Cursor() uint64
	Subscribe(after uint64) (Subscription, error)
}

// CloneScope detaches every mutable correlation value from its caller.
func CloneScope(scope *Scope) *Scope {
	if scope == nil {
		return nil
	}
	result := *scope
	if scope.Agent != nil {
		agent := *scope.Agent
		if agent.RoundIndex != nil {
			index := *agent.RoundIndex
			agent.RoundIndex = &index
		}
		result.Agent = &agent
	}
	if scope.Operation != nil {
		operation := *scope.Operation
		result.Operation = &operation
	}
	return &result
}

package appcli

import (
	"context"

	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
)

// TurnState describes how the active agent turn finished.
type TurnState uint8

const (
	// TurnCompleted indicates a successful agent turn.
	TurnCompleted TurnState = iota + 1
	// TurnCanceled indicates that the user canceled the active turn.
	TurnCanceled
	// TurnFailed indicates that the active turn returned an error.
	TurnFailed
)

// InterruptKind identifies an action that remains available while an agent
// turn is running.
type InterruptKind uint8

const (
	// InterruptCancel cancels the active turn and keeps the frontend running.
	InterruptCancel InterruptKind = iota + 1
	// InterruptExit cancels the active turn and requests normal process exit.
	InterruptExit
)

// Interrupt is emitted by an interactive frontend while a turn is active.
type Interrupt struct{ Kind InterruptKind }

// SessionView is a caller-owned snapshot used to replace the frontend's
// visible session state. Frontends copy any mutable values retained after
// Sync returns.
type SessionView struct {
	Current  session.ID
	Sessions []session.Metadata
	Messages []model.Message
}

// Frontend is the app.cli-local transport used by the application loop. It
// extends line input with structured state synchronization and turn controls;
// it is deliberately not part of the public SDK interaction contract.
type Frontend interface {
	LineInput
	Sync(context.Context, SessionView) error
	StartTurn(context.Context, string) error
	FinishTurn(context.Context, TurnState) error
	Interrupts() <-chan Interrupt
}

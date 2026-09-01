package agentdefault

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/ingot-agent/sdk/observation"
)

type discardObservation struct{}

func (discardObservation) Emit(context.Context, observation.Detail) {}

func newTurnID() (observation.ID, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return observation.ID(hex.EncodeToString(raw[:])), nil
}

func terminalStatus(err error) observation.Status {
	if err == nil {
		return observation.StatusSucceeded
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return observation.StatusCanceled
	}
	return observation.StatusFailed
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

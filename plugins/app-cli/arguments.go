package appcli

import (
	"errors"
	"fmt"
)

// ErrInvalidArguments indicates an unsupported app.cli process invocation.
var ErrInvalidArguments = errors.New("invalid app.cli arguments")

// Mode selects the concrete terminal frontend.
type Mode uint8

const (
	// ModeTUI uses the full-screen terminal frontend.
	ModeTUI Mode = iota + 1
	// ModePlain uses cancellable line-oriented input and plain output.
	ModePlain
)

// ParseArguments accepts the public `chat [--plain]` runtime command.
func ParseArguments(arguments []string) (Mode, error) {
	if len(arguments) == 1 && arguments[0] == "chat" {
		return ModeTUI, nil
	}
	if len(arguments) == 2 && arguments[0] == "chat" && arguments[1] == "--plain" {
		return ModePlain, nil
	}
	return 0, fmt.Errorf("usage: ingot chat [--plain]: %w", ErrInvalidArguments)
}

// Package sessionjsonl provides append-oriented JSONL session persistence.
package sessionjsonl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/session"
)

var (
	// ErrInvalidConfig indicates that the durability configuration is invalid.
	ErrInvalidConfig = errors.New("invalid session.jsonl config")
	// ErrInvalidSessionID indicates that an ID is unsafe or malformed.
	ErrInvalidSessionID = errors.New("invalid session id")
	// ErrInvalidEntry indicates that an entry cannot be persisted.
	ErrInvalidEntry = errors.New("invalid session entry")
	// ErrCorruptState indicates that persisted state violates the v1 format.
	ErrCorruptState = errors.New("corrupt session state")
	// ErrUnsupportedState indicates that the state schema is outside the reader window.
	ErrUnsupportedState = errors.New("unsupported session state version")
	// ErrInvalidQuery indicates negative pagination values.
	ErrInvalidQuery = errors.New("invalid session query")
	// ErrStateDirLocked indicates that another writer owns the StateDir.
	ErrStateDirLocked = errors.New("session state directory is locked")
	// ErrInvalidDependencies indicates that required host dependencies are
	// missing.
	ErrInvalidDependencies = errors.New("invalid session.jsonl dependencies")
	// ErrOwnerLockUnsupported indicates that this platform cannot provide the
	// owner lock required by session.jsonl v0.1.
	ErrOwnerLockUnsupported = errors.New("session state owner lock unsupported")
	// ErrCommitUnknown indicates that an Append may have reached the file even
	// though the operation returned an error.
	ErrCommitUnknown = errors.New("session append commit status unknown")
)

// Config configures the persistence durability policy.
type Config struct {
	Durability string `toml:"durability"`
}

// Dependencies contains the component's consumed capabilities.
type Dependencies struct {
	// State is the plugin-scoped persistent state directory assigned by the
	// generated runtime.
	State state.Scope
}

// Exports contains the component's provided capabilities.
type Exports struct {
	Store session.MutableStore
}

// New opens or initializes the plugin-scoped state directory.
func New(
	ctx context.Context,
	cfg Config,
	deps Dependencies,
) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	if isNil(deps.State) {
		return Exports{}, nil, fmt.Errorf("State scope is required: %w", ErrInvalidDependencies)
	}
	durability := cfg.Durability
	if durability == "" {
		durability = "sync"
	}
	if durability != "sync" && durability != "flush" {
		return Exports{}, nil, fmt.Errorf("durability %q, want sync or flush: %w", durability, ErrInvalidConfig)
	}
	stateDir := deps.State.Dir()
	created, release, err := openStore(ctx, stateDir, durability == "sync")
	if err != nil {
		return Exports{}, nil, err
	}
	cleanup := ingotabi.Cleanup(func(cleanupCtx context.Context) error {
		releaseErr := release()
		if cleanupCtx == nil {
			return errors.Join(context.Canceled, releaseErr)
		}
		if ctxErr := cleanupCtx.Err(); ctxErr != nil {
			return errors.Join(ctxErr, releaseErr)
		}
		return releaseErr
	})
	return Exports{Store: created}, cleanup, nil
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func exactJSON(data []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func removeCandidate(path string) {
	if path != "" {
		_ = os.RemoveAll(path)
	}
}

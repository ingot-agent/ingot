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

	"github.com/ingot-agent/sdk"
	"github.com/ingot-agent/sdk/config"
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
type Dependencies struct{}

// Exports contains the component's provided capabilities.
type Exports struct {
	Store session.Store
}

// New opens or initializes the plugin-scoped state directory.
func New(
	ctx context.Context,
	cfg Config,
	_ Dependencies,
) (Exports, sdk.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	durability := cfg.Durability
	if durability == "" {
		durability = "sync"
	}
	if durability != "sync" && durability != "flush" {
		return Exports{}, nil, fmt.Errorf("durability %q, want sync or flush: %w", durability, ErrInvalidConfig)
	}
	stateDir, err := config.StateDir(ctx)
	if err != nil {
		return Exports{}, nil, fmt.Errorf("open session state: %w", err)
	}
	created, release, err := openStore(ctx, stateDir, durability == "sync")
	if err != nil {
		return Exports{}, nil, err
	}
	cleanup := sdk.Cleanup(func(cleanupCtx context.Context) error {
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

package sessionsqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/session"
	sessionsqlite "github.com/ingot-agent/session-sqlite"
)

type stateScope string

func (scope stateScope) Dir() string { return string(scope) }

func TestPublicComponentContract(t *testing.T) {
	exports, cleanup, err := sessionsqlite.New(context.Background(), sessionsqlite.Config{}, sessionsqlite.Dependencies{
		State: stateScope(filepath.Join(t.TempDir(), "state")),
	})
	if err != nil {
		t.Fatal(err)
	}
	var (
		_ session.Store   = exports.Store
		_ session.Manager = exports.Manager
		_ session.Query   = exports.Query
	)
	if cleanup == nil {
		t.Fatal("cleanup is nil")
	}
	if err := cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestNewRejectsInvalidDependencies(t *testing.T) {
	var typedNil *scope
	for _, dependency := range []state.Scope{nil, typedNil, stateScope("relative")} {
		if _, _, err := sessionsqlite.New(context.Background(), sessionsqlite.Config{}, sessionsqlite.Dependencies{State: dependency}); !errors.Is(err, sessionsqlite.ErrInvalidDependencies) {
			t.Fatalf("State=%#v error=%v", dependency, err)
		}
	}
}

type scope struct{ root string }

func (value *scope) Dir() string { return value.root }

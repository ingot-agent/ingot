// Package sessionsqlite implements transactional local session persistence in
// a plugin-scoped SQLite database.
package sessionsqlite

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/session"
)

var (
	// ErrInvalidDependencies indicates that the runtime did not provide a
	// usable plugin-scoped state directory.
	ErrInvalidDependencies = errors.New("invalid session.sqlite dependencies")
	// ErrUnsupportedSchema indicates that the database uses an unsupported
	// application schema version.
	ErrUnsupportedSchema = errors.New("unsupported session.sqlite schema")
)

// Config is reserved for future persistence policy. M5 has no configurable
// behavior.
type Config struct{}

// Dependencies contains the plugin-scoped persistent state location.
type Dependencies struct {
	State state.Scope
}

// Exports exposes the three independent session capabilities backed by one
// transactional store.
type Exports struct {
	Store   session.Store
	Manager session.Manager
	Query   session.Query
}

// New opens or creates the plugin-scoped session database. The returned
// cleanup closes the database and prevents new operations.
func New(ctx context.Context, _ Config, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil || isNil(deps.State) {
		return Exports{}, nil, fmt.Errorf("construct session.sqlite: %w", ErrInvalidDependencies)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	root := deps.State.Dir()
	if root == "" || !filepath.IsAbs(root) {
		return Exports{}, nil, fmt.Errorf("state directory must be absolute and non-empty: %w", ErrInvalidDependencies)
	}
	created, err := openStore(ctx, filepath.Join(filepath.Clean(root), "sessions.sqlite3"))
	if err != nil {
		return Exports{}, nil, err
	}
	cleanup := ingotabi.Cleanup(func(cleanupCtx context.Context) error {
		closeErr := created.close()
		if cleanupCtx == nil {
			return errors.Join(context.Canceled, closeErr)
		}
		return errors.Join(cleanupCtx.Err(), closeErr)
	})
	return Exports{Store: created, Manager: created, Query: created}, cleanup, nil
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

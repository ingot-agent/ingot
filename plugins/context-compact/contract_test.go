package contextcompact_test

import (
	"context"
	"testing"

	contextcompact "github.com/ingot-agent/context-compact"
	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
)

type contractModel struct{}

func (contractModel) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, nil
}

type contractStore struct{}

func (contractStore) Create(context.Context, session.Metadata) (session.ID, error) { return "", nil }
func (contractStore) Append(context.Context, session.ID, session.Entry) error      { return nil }
func (contractStore) Load(context.Context, session.ID) ([]session.Entry, error)    { return nil, nil }
func (contractStore) List(context.Context, session.Query) ([]session.Summary, error) {
	return nil, nil
}

func TestPublicComponentContract(t *testing.T) {
	t.Parallel()
	exports, cleanup, err := contextcompact.New(
		context.Background(),
		contextcompact.Config{TriggerRequestBytes: 1024, TargetRequestBytes: 512},
		contextcompact.Dependencies{Model: contractModel{}, Store: contractStore{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		t.Fatal("cleanup must be nil")
	}
	var _ contextwindow.Compactor = exports.Compactor
}

var _ func(context.Context, contextcompact.Config, contextcompact.Dependencies) (contextcompact.Exports, ingotabi.Cleanup, error) = contextcompact.New

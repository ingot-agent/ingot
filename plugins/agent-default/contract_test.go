package agentdefault_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	agentdefault "github.com/ingot-agent/agent-default"
	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/asset"
	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/prompt"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

type modelRuntime struct{}

func (modelRuntime) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, nil
}

type toolRuntime struct{}

func (toolRuntime) Definitions() []tool.Definition { return nil }
func (toolRuntime) Call(context.Context, tool.Call) (tool.Result, error) {
	return tool.Result{}, nil
}

type sessionStore struct{}

func (sessionStore) Create(context.Context, session.Metadata) (session.ID, error) { return "s", nil }
func (sessionStore) Append(context.Context, session.ID, session.Entry) error      { return nil }
func (sessionStore) Load(context.Context, session.ID) ([]session.Entry, error)    { return nil, nil }
func (sessionStore) List(context.Context, session.Query) ([]session.Summary, error) {
	return nil, nil
}

type assetStore struct{}

func (assetStore) Put(context.Context, asset.PutRequest) (asset.Reference, asset.Info, error) {
	return asset.Reference{ID: "asset"}, asset.Info{}, nil
}
func (assetStore) Stat(context.Context, asset.Reference) (asset.Info, error) {
	return asset.Info{}, nil
}
func (assetStore) Open(context.Context, asset.Reference) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

type promptRenderer struct{}

func (promptRenderer) Render(context.Context, prompt.Request) ([]model.Message, error) {
	return nil, nil
}

type contextCompactor struct{}

func (contextCompactor) Compact(context.Context, contextwindow.CompactionRequest) (contextwindow.CompactionResult, error) {
	return contextwindow.CompactionResult{}, nil
}

func TestComponentContractIncludesOptionalCompactor(t *testing.T) {
	exports, cleanup, err := agentdefault.New(context.Background(), agentdefault.Config{}, agentdefault.Dependencies{
		Model: modelRuntime{}, Tools: toolRuntime{}, Store: sessionStore{}, Assets: assetStore{}, Prompt: promptRenderer{},
		Compactor: ingotabi.Some[contextwindow.Compactor](contextCompactor{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		t.Fatal("cleanup must be nil")
	}
	var runtime agent.Runtime = exports.Runtime
	if runtime == nil {
		t.Fatal("runtime is nil")
	}
}

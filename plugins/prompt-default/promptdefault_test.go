package promptdefault_test

import (
	"context"
	"errors"
	"testing"

	promptdefault "github.com/ingot-agent/prompt-default"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/prompt"
	"github.com/ingot-agent/sdk/tool"
)

type contributorFunc func(context.Context, prompt.Request) ([]prompt.Block, error)

func (f contributorFunc) Contribute(ctx context.Context, request prompt.Request) ([]prompt.Block, error) {
	return f(ctx, request)
}

func TestRendererFormatsInStableOrderAndIsolatesContributors(t *testing.T) {
	originalArguments := []byte(`{"x":1}`)
	first := contributorFunc(func(_ context.Context, request prompt.Request) ([]prompt.Block, error) {
		request.History[0].ToolCalls[0].Arguments[0] = 'X'
		return []prompt.Block{{Name: "first", Content: "one"}}, nil
	})
	second := contributorFunc(func(_ context.Context, request prompt.Request) ([]prompt.Block, error) {
		if string(request.History[0].ToolCalls[0].Arguments) != `{"x":1}` {
			t.Fatalf("second contributor saw mutation: %s", request.History[0].ToolCalls[0].Arguments)
		}
		return []prompt.Block{{Name: "second", Content: "two"}}, nil
	})
	exports, _, err := promptdefault.New(context.Background(), promptdefault.Config{SystemPrompt: "base"}, promptdefault.Dependencies{Contributors: []prompt.Contributor{first, second}})
	if err != nil {
		t.Fatal(err)
	}
	history := []model.Message{{Role: model.RoleAssistant, Content: "old", ToolCalls: []tool.Call{{ID: "id", Name: "t", Arguments: originalArguments}}}}
	messages, err := exports.Renderer.Render(context.Background(), prompt.Request{Input: "new", History: history})
	if err != nil {
		t.Fatal(err)
	}
	wantSystem := "base\n\n## first\none\n\n## second\ntwo"
	if len(messages) != 3 || messages[0].Content != wantSystem || messages[1].Content != "old" || messages[2].Role != model.RoleUser || messages[2].Content != "new" {
		t.Fatalf("messages=%#v", messages)
	}
	if string(originalArguments) != `{"x":1}` {
		t.Fatalf("caller history mutated: %s", originalArguments)
	}
}

func TestRendererCountsFormattingInTotalLimit(t *testing.T) {
	contributor := contributorFunc(func(context.Context, prompt.Request) ([]prompt.Block, error) {
		return []prompt.Block{{Name: "x", Content: "y"}}, nil
	})
	exports, _, err := promptdefault.New(context.Background(), promptdefault.Config{SystemPrompt: "a", MaxSystemBytes: 8}, promptdefault.Dependencies{Contributors: []prompt.Contributor{contributor}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = exports.Renderer.Render(context.Background(), prompt.Request{})
	if !errors.Is(err, promptdefault.ErrSystemLimit) {
		t.Fatalf("limit error=%v", err)
	}
	_, _, err = promptdefault.New(context.Background(), promptdefault.Config{SystemPrompt: "abcd", MaxSystemBytes: 3}, promptdefault.Dependencies{})
	if !errors.Is(err, promptdefault.ErrSystemLimit) || !errors.Is(err, promptdefault.ErrInvalidConfig) {
		t.Fatalf("config limit error=%v", err)
	}
}

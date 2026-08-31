package promptdefault_test

import (
	"context"
	"errors"
	"testing"

	promptdefault "github.com/ingot-agent/prompt-default"
	"github.com/ingot-agent/sdk/content"
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
		return []prompt.Block{{Name: "first", Content: content.FromText("one")}}, nil
	})
	second := contributorFunc(func(_ context.Context, request prompt.Request) ([]prompt.Block, error) {
		if string(request.History[0].ToolCalls[0].Arguments) != `{"x":1}` {
			t.Fatalf("second contributor saw mutation: %s", request.History[0].ToolCalls[0].Arguments)
		}
		return []prompt.Block{{Name: "second", Content: content.FromText("two")}}, nil
	})
	exports, _, err := promptdefault.New(context.Background(), promptdefault.Config{SystemPrompt: "base"}, promptdefault.Dependencies{Contributors: []prompt.Contributor{first, second}})
	if err != nil {
		t.Fatal(err)
	}
	history := []model.Message{{Role: model.RoleAssistant, Content: content.FromText("old"), ToolCalls: []tool.Call{{ID: "id", Name: "t", Arguments: originalArguments}}}}
	messages, err := exports.Renderer.Render(context.Background(), prompt.Request{Input: content.FromText("new"), History: history})
	if err != nil {
		t.Fatal(err)
	}
	wantSystem := "base\n\n## first\none\n\n## second\ntwo"
	systemText, systemOK := content.TextOnly(messages[0].Content)
	oldText, oldOK := content.TextOnly(messages[1].Content)
	newText, newOK := content.TextOnly(messages[2].Content)
	if len(messages) != 3 || !systemOK || systemText != wantSystem || !oldOK || oldText != "old" || messages[2].Role != model.RoleUser || !newOK || newText != "new" {
		t.Fatalf("messages=%#v", messages)
	}
	if string(originalArguments) != `{"x":1}` {
		t.Fatalf("caller history mutated: %s", originalArguments)
	}
}

func TestRendererCountsFormattingInTotalLimit(t *testing.T) {
	contributor := contributorFunc(func(context.Context, prompt.Request) ([]prompt.Block, error) {
		return []prompt.Block{{Name: "x", Content: content.FromText("y")}}, nil
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

func TestRendererPreservesMultimodalBlockAndInputOrder(t *testing.T) {
	blockData := []byte{1, 2}
	inputData := []byte{3, 4}
	contributor := contributorFunc(func(_ context.Context, request prompt.Request) ([]prompt.Block, error) {
		request.Input[1].Media.Source.Data[0] = 9
		return []prompt.Block{{Name: "vision", Content: content.Content{
			content.Text("before"),
			content.Inline(content.KindImage, "image/png", "block.png", blockData),
			content.Text("after"),
		}}}, nil
	})
	exports, _, err := promptdefault.New(context.Background(), promptdefault.Config{}, promptdefault.Dependencies{Contributors: []prompt.Contributor{contributor}})
	if err != nil {
		t.Fatal(err)
	}
	input := content.Content{content.Text("describe"), content.Inline(content.KindImage, "image/png", "input.png", inputData)}
	messages, err := exports.Renderer.Render(context.Background(), prompt.Request{Input: input})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || len(messages[0].Content) != 4 || messages[0].Content[0].Text != "## vision\n" || messages[0].Content[1].Text != "before" || messages[0].Content[2].Kind != content.KindImage || messages[0].Content[3].Text != "after" {
		t.Fatalf("system content = %#v", messages)
	}
	if len(messages[1].Content) != 2 || messages[1].Content[0].Text != "describe" || messages[1].Content[1].Media.Source.Data[0] != 3 {
		t.Fatalf("user content = %#v", messages[1].Content)
	}
	blockData[0] = 8
	inputData[0] = 8
	if messages[0].Content[2].Media.Source.Data[0] != 1 || messages[1].Content[1].Media.Source.Data[0] != 3 {
		t.Fatal("renderer returned aliased inline data")
	}
}

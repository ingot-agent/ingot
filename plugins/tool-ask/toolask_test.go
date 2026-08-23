package toolask

import (
	"context"
	"errors"
	"testing"

	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/tool"
)

type fakeChannel struct {
	prompt   string
	response string
	err      error
}

func (c *fakeChannel) Ask(_ context.Context, request interaction.AskRequest) (interaction.AskResponse, error) {
	c.prompt = request.Prompt
	return interaction.AskResponse{Text: c.response}, c.err
}
func (*fakeChannel) ReadLine(context.Context, string) (string, error) { return "", nil }
func (*fakeChannel) Render(context.Context, interaction.Event) error  { return nil }

func TestAskUserPassesPromptAndReturnsResponse(t *testing.T) {
	channel := &fakeChannel{response: "approved"}
	exports, _, err := New(context.Background(), Config{}, Dependencies{Interaction: channel})
	if err != nil {
		t.Fatal(err)
	}
	result, err := exports.Tools[0].Invoke(context.Background(), tool.Call{Name: "ask_user", Arguments: []byte("{\"prompt\":\"Continue?\"}")})
	if err != nil || result.Content != "approved" || channel.prompt != "Continue?" {
		t.Fatalf("result=%#v err=%v prompt=%q", result, err, channel.prompt)
	}
}

func TestAskUserLimitsAndUnavailable(t *testing.T) {
	channel := &fakeChannel{response: "ok"}
	exports, _, err := New(context.Background(), Config{MaxPromptBytes: 3}, Dependencies{Interaction: channel})
	if err != nil {
		t.Fatal(err)
	}
	_, err = exports.Tools[0].Invoke(context.Background(), tool.Call{Arguments: []byte("{\"prompt\":\"long\"}")})
	if !errors.Is(err, ErrPromptLimit) {
		t.Fatalf("prompt limit = %v", err)
	}
	channel.err = interaction.ErrUnavailable
	exports, _, err = New(context.Background(), Config{}, Dependencies{Interaction: channel})
	if err != nil {
		t.Fatal(err)
	}
	_, err = exports.Tools[0].Invoke(context.Background(), tool.Call{Arguments: []byte("{\"prompt\":\"ok\"}")})
	if !errors.Is(err, interaction.ErrUnavailable) {
		t.Fatalf("unavailable = %v", err)
	}
}

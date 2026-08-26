package toolask

import (
	"context"
	"errors"
	"testing"

	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/tool"
)

type fakeChannel struct {
	request  interaction.AskRequest
	response string
	err      error
}

func (c *fakeChannel) Ask(_ context.Context, request interaction.AskRequest) (interaction.AskResponse, error) {
	c.request = request
	return interaction.AskResponse{Text: c.response}, c.err
}
func (*fakeChannel) Render(context.Context, interaction.Event) error  { return nil }

func TestAskUserPassesPromptAndReturnsResponse(t *testing.T) {
	channel := &fakeChannel{response: "approved"}
	exports, _, err := New(context.Background(), Config{}, Dependencies{Interaction: channel})
	if err != nil {
		t.Fatal(err)
	}
	result, err := exports.Tools[0].Invoke(context.Background(), tool.Call{Name: "ask_user", Arguments: []byte("{\"prompt\":\"Continue?\"}")})
	if err != nil || result.Content != "approved" || channel.request.Prompt != "Continue?" {
		t.Fatalf("result=%#v err=%v request=%#v", result, err, channel.request)
	}
	if channel.request.Options != nil || channel.request.AllowTextInput {
		t.Fatalf("plain text request=%#v", channel.request)
	}
}

func TestAskUserPassesOptionsAndEnablesFreeText(t *testing.T) {
	channel := &fakeChannel{response: "a custom answer"}
	exports, _, err := New(context.Background(), Config{}, Dependencies{Interaction: channel})
	if err != nil {
		t.Fatal(err)
	}
	arguments := []byte(`{"prompt":"Choose a deployment","options":[{"label":"Staging","description":"Deploy for verification"},{"label":"Production"}]}`)
	result, err := exports.Tools[0].Invoke(context.Background(), tool.Call{Name: "ask_user", Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "a custom answer" || !channel.request.AllowTextInput {
		t.Fatalf("result=%#v request=%#v", result, channel.request)
	}
	want := []interaction.AskOption{
		{Label: "Staging", Description: "Deploy for verification"},
		{Label: "Production"},
	}
	if len(channel.request.Options) != len(want) {
		t.Fatalf("options=%#v", channel.request.Options)
	}
	for index := range want {
		if channel.request.Options[index] != want[index] {
			t.Fatalf("option[%d]=%#v want=%#v", index, channel.request.Options[index], want[index])
		}
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

func TestAskUserRejectsInvalidAndOversizedOptions(t *testing.T) {
	channel := &fakeChannel{response: "ok"}
	exports, _, err := New(context.Background(), Config{MaxOptions: 1, MaxOptionsBytes: 4}, Dependencies{Interaction: channel})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		arguments string
		want      error
	}{
		{name: "empty", arguments: `{"prompt":"p","options":[]}`, want: ErrInvalidArguments},
		{name: "null", arguments: `{"prompt":"p","options":null}`, want: ErrInvalidArguments},
		{name: "count", arguments: `{"prompt":"p","options":[{"label":"a"},{"label":"b"}]}`, want: ErrOptionsLimit},
		{name: "bytes", arguments: `{"prompt":"p","options":[{"label":"large"}]}`, want: ErrOptionsLimit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := exports.Tools[0].Invoke(context.Background(), tool.Call{Arguments: []byte(test.arguments)})
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want chain %v", err, test.want)
			}
		})
	}

	exports, _, err = New(context.Background(), Config{}, Dependencies{Interaction: channel})
	if err != nil {
		t.Fatal(err)
	}
	_, err = exports.Tools[0].Invoke(context.Background(), tool.Call{Arguments: []byte(`{"prompt":"p","options":[{"label":"same"},{"label":"same"}]}`)})
	if !errors.Is(err, ErrInvalidArguments) {
		t.Fatalf("duplicate error=%v", err)
	}
}

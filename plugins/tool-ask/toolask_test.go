package toolask

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/tool"
)

type fakeChannel struct {
	request        interaction.Request
	response       string
	customResponse *interaction.Response
	err            error
}

func (c *fakeChannel) Request(_ context.Context, request interaction.Request) (interaction.Response, error) {
	c.request = request
	if c.customResponse != nil {
		return *c.customResponse, c.err
	}
	return interaction.Response{Values: []interaction.Answer{{Name: answerFieldName, Value: interaction.StringValue(c.response)}}}, c.err
}
func (*fakeChannel) Emit(context.Context, interaction.Event) error { return nil }
func (*fakeChannel) Set(context.Context, interaction.State) error  { return nil }
func (*fakeChannel) Clear(context.Context, string) error           { return nil }

func resultText(result tool.Result) string {
	value, _ := content.TextOnly(result.Content)
	return value
}

func TestAskUserPassesPromptAndReturnsResponse(t *testing.T) {
	channel := &fakeChannel{response: "approved"}
	exports, _, err := New(context.Background(), Config{}, Dependencies{Interaction: channel})
	if err != nil {
		t.Fatal(err)
	}
	result, err := exports.Tools[0].Invoke(context.Background(), tool.Call{Name: "ask_user", Arguments: []byte("{\"prompt\":\"Continue?\"}")})
	if text, ok := content.TextOnly(result.Content); err != nil || !ok || text != "approved" || channel.request.Name != requestName || channel.request.Description != "Continue?" {
		t.Fatalf("result=%#v err=%v request=%#v", result, err, channel.request)
	}
	if len(channel.request.Fields) != 1 || channel.request.Fields[0].Name != answerFieldName || channel.request.Fields[0].Kind != interaction.FieldString || channel.request.Fields[0].Options != nil {
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
	if text, ok := content.TextOnly(result.Content); !ok || text != "a custom answer" || len(channel.request.Fields) != 1 {
		t.Fatalf("result=%#v request=%#v", result, channel.request)
	}
	want := []interaction.Option{
		{Value: "Staging", Label: "Staging", Description: "Deploy for verification"},
		{Value: "Production", Label: "Production"},
	}
	options := channel.request.Fields[0].Options
	if len(options) != len(want) {
		t.Fatalf("options=%#v", options)
	}
	for index := range want {
		if options[index] != want[index] {
			t.Fatalf("option[%d]=%#v want=%#v", index, options[index], want[index])
		}
	}
}

func TestAskUserLimitsAndUnavailable(t *testing.T) {
	channel := &fakeChannel{response: "ok"}
	exports, _, err := New(context.Background(), Config{MaxPromptBytes: 3}, Dependencies{Interaction: channel})
	if err != nil {
		t.Fatal(err)
	}
	result, err := exports.Tools[0].Invoke(context.Background(), tool.Call{Arguments: []byte("{\"prompt\":\"long\"}")})
	if err != nil {
		t.Fatalf("prompt limit should be a result, got error: %v", err)
	}
	if !strings.Contains(resultText(result), "prompt exceeds configured limit") {
		t.Fatalf("prompt limit result = %q", resultText(result))
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
	invalidArgs := []struct {
		name      string
		arguments string
	}{
		{name: "empty", arguments: `{"prompt":"p","options":[]}`},
		{name: "null", arguments: `{"prompt":"p","options":null}`},
	}
	for _, test := range invalidArgs {
		t.Run(test.name, func(t *testing.T) {
			_, err := exports.Tools[0].Invoke(context.Background(), tool.Call{Arguments: []byte(test.arguments)})
			if !errors.Is(err, ErrInvalidArguments) {
				t.Fatalf("error=%v want chain %v", err, ErrInvalidArguments)
			}
		})
	}
	limits := []struct {
		name      string
		arguments string
	}{
		{name: "count", arguments: `{"prompt":"p","options":[{"label":"a"},{"label":"b"}]}`},
		{name: "bytes", arguments: `{"prompt":"p","options":[{"label":"large"}]}`},
	}
	for _, test := range limits {
		t.Run(test.name, func(t *testing.T) {
			result, err := exports.Tools[0].Invoke(context.Background(), tool.Call{Arguments: []byte(test.arguments)})
			if err != nil {
				t.Fatalf("options limit should be a result, got error: %v", err)
			}
			if !strings.Contains(resultText(result), "options exceed configured limit") {
				t.Fatalf("options limit result = %q", resultText(result))
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

func TestAskUserRejectsMalformedInteractionResponse(t *testing.T) {
	tests := []struct {
		name     string
		response interaction.Response
	}{
		{name: "missing answer"},
		{name: "wrong kind", response: interaction.Response{Values: []interaction.Answer{{Name: answerFieldName, Value: interaction.IntegerValue(1)}}}},
		{name: "duplicate answer", response: interaction.Response{Values: []interaction.Answer{
			{Name: answerFieldName, Value: interaction.StringValue("first")},
			{Name: answerFieldName, Value: interaction.StringValue("second")},
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := test.response
			channel := &fakeChannel{customResponse: &response}
			exports, _, err := New(context.Background(), Config{}, Dependencies{Interaction: channel})
			if err != nil {
				t.Fatal(err)
			}
			result, err := exports.Tools[0].Invoke(context.Background(), tool.Call{Arguments: []byte(`{"prompt":"question"}`)})
			if err != nil {
				t.Fatalf("invalid response should be a result, got error: %v", err)
			}
			if !strings.Contains(resultText(result), "invalid tool.ask interaction response") {
				t.Fatalf("invalid response result = %q", resultText(result))
			}
		})
	}
}

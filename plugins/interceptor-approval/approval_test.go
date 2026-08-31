package approval

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/pipeline"
	"github.com/ingot-agent/sdk/tool"
)

type queueChannel struct {
	responses []string
	prompts   []string
	requests  []interaction.Request
}

func (c *queueChannel) Request(_ context.Context, request interaction.Request) (interaction.Response, error) {
	c.prompts = append(c.prompts, request.Description)
	c.requests = append(c.requests, request)
	if len(c.responses) == 0 {
		return interaction.Response{}, nil
	}
	response := c.responses[0]
	c.responses = c.responses[1:]
	return interaction.Response{Values: []interaction.Answer{{Name: decisionFieldName, Value: interaction.StringValue(response)}}}, nil
}
func (*queueChannel) Emit(context.Context, interaction.Event) error { return nil }
func (*queueChannel) Set(context.Context, interaction.State) error  { return nil }
func (*queueChannel) Clear(context.Context, string) error           { return nil }

func terminal(counter *int) pipeline.Next[tool.Call, tool.Result] {
	return func(_ context.Context, _ tool.Call) (tool.Result, error) {
		*counter++
		return tool.Result{Content: content.FromText("ok")}, nil
	}
}

func TestApprovalActionsAndRules(t *testing.T) {
	channel := &queueChannel{responses: []string{"maybe", actionAllow}}
	exports, _, err := New(context.Background(), Config{DefaultAction: "deny", Rules: []Rule{{Tool: "safe", Action: "allow"}, {Tool: "danger", Action: "ask"}}}, Dependencies{Interaction: ingotabi.Some[interaction.Channel](channel)})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	next := terminal(&count)
	result, err := exports.Interceptors[0].Invoke(context.Background(), tool.Call{Name: "safe"}, next)
	if text, ok := content.TextOnly(result.Content); err != nil || !ok || text != "ok" || count != 1 {
		t.Fatalf("allow result=%#v err=%v count=%d", result, err, count)
	}
	_, err = exports.Interceptors[0].Invoke(context.Background(), tool.Call{Name: "blocked"}, next)
	if !errors.Is(err, ErrApprovalDenied) || count != 1 {
		t.Fatalf("deny error=%v count=%d", err, count)
	}
	result, err = exports.Interceptors[0].Invoke(context.Background(), tool.Call{ID: "c1", Name: "danger", Arguments: []byte("{\"path\":\"x\"}")}, next)
	if text, ok := content.TextOnly(result.Content); err != nil || !ok || text != "ok" || len(channel.prompts) != 2 {
		t.Fatalf("ask result=%#v err=%v prompts=%d", result, err, len(channel.prompts))
	}
	if !strings.Contains(channel.prompts[0], "danger") || !strings.Contains(channel.prompts[0], "c1") || !strings.Contains(channel.prompts[0], "{\"path\":\"x\"}") {
		t.Fatalf("prompt=%q", channel.prompts[0])
	}
	request := channel.requests[0]
	if request.Name != requestName || request.Level != interaction.LevelWarning || len(request.Fields) != 1 {
		t.Fatalf("request=%#v", request)
	}
	options := request.Fields[0].Options
	if request.Fields[0].Name != decisionFieldName || request.Fields[0].Kind != interaction.FieldChoice || len(options) != 2 ||
		options[0].Value != actionAllow || options[0].Label != "Yes" || options[1].Value != actionDeny || options[1].Label != "No" {
		t.Fatalf("field=%#v", request.Fields[0])
	}
}

func TestApprovalFailsClosedAndRetries(t *testing.T) {
	exports, _, err := New(context.Background(), Config{}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = exports.Interceptors[0].Invoke(context.Background(), tool.Call{Name: "x"}, terminal(new(int)))
	if !errors.Is(err, interaction.ErrUnavailable) {
		t.Fatalf("missing channel error=%v", err)
	}
	channel := &queueChannel{responses: []string{"what", "still", "unknown"}}
	exports, _, err = New(context.Background(), Config{MaxDisplayBytes: 14}, Dependencies{Interaction: ingotabi.Some[interaction.Channel](channel)})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	_, err = exports.Interceptors[0].Invoke(context.Background(), tool.Call{Name: "x", Arguments: []byte("{\"long\":\"value\"}")}, terminal(&count))
	if !errors.Is(err, ErrApprovalDenied) || count != 0 || len(channel.prompts) != maxAttempts {
		t.Fatalf("retry error=%v count=%d prompts=%d", err, count, len(channel.prompts))
	}
	if !strings.Contains(channel.prompts[0], "...[truncated]") {
		t.Fatalf("prompt not truncated: %q", channel.prompts[0])
	}
}

func TestApprovalPreservesCanceledContext(t *testing.T) {
	exports, _, err := New(context.Background(), Config{DefaultAction: "allow"}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = exports.Interceptors[0].Invoke(ctx, tool.Call{Name: "safe"}, terminal(new(int)))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestResponseStringRejectsMalformedAnswers(t *testing.T) {
	tests := []struct {
		name     string
		response interaction.Response
	}{
		{name: "missing"},
		{name: "wrong kind", response: interaction.Response{Values: []interaction.Answer{{Name: decisionFieldName, Value: interaction.BooleanValue(true)}}}},
		{name: "duplicate", response: interaction.Response{Values: []interaction.Answer{
			{Name: decisionFieldName, Value: interaction.StringValue(actionAllow)},
			{Name: decisionFieldName, Value: interaction.StringValue(actionDeny)},
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if value, ok := responseString(test.response, decisionFieldName); ok {
				t.Fatalf("responseString()=(%q, true), want invalid", value)
			}
		})
	}
}

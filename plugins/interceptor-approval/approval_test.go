package approval

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ingot-agent/sdk"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/pipeline"
	"github.com/ingot-agent/sdk/tool"
)

type queueChannel struct {
	responses []string
	prompts   []string
}

func (c *queueChannel) Ask(_ context.Context, request interaction.AskRequest) (interaction.AskResponse, error) {
	c.prompts = append(c.prompts, request.Prompt)
	if len(c.responses) == 0 {
		return interaction.AskResponse{}, nil
	}
	response := c.responses[0]
	c.responses = c.responses[1:]
	return interaction.AskResponse{Text: response}, nil
}
func (*queueChannel) ReadLine(context.Context, string) (string, error) { return "", nil }
func (*queueChannel) Render(context.Context, interaction.Event) error  { return nil }

func terminal(counter *int) pipeline.Next[tool.Call, tool.Result] {
	return func(_ context.Context, _ tool.Call) (tool.Result, error) {
		*counter++
		return tool.Result{Content: "ok"}, nil
	}
}

func TestApprovalActionsAndRules(t *testing.T) {
	channel := &queueChannel{responses: []string{"maybe", "YES"}}
	exports, _, err := New(context.Background(), Config{DefaultAction: "deny", Rules: []Rule{{Tool: "safe", Action: "allow"}, {Tool: "danger", Action: "ask"}}}, Dependencies{Interaction: sdk.Some[interaction.Channel](channel)})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	next := terminal(&count)
	result, err := exports.Interceptors[0].Invoke(context.Background(), tool.Call{Name: "safe"}, next)
	if err != nil || result.Content != "ok" || count != 1 {
		t.Fatalf("allow result=%#v err=%v count=%d", result, err, count)
	}
	_, err = exports.Interceptors[0].Invoke(context.Background(), tool.Call{Name: "blocked"}, next)
	if !errors.Is(err, ErrApprovalDenied) || count != 1 {
		t.Fatalf("deny error=%v count=%d", err, count)
	}
	result, err = exports.Interceptors[0].Invoke(context.Background(), tool.Call{ID: "c1", Name: "danger", Arguments: []byte("{\"path\":\"x\"}")}, next)
	if err != nil || result.Content != "ok" || len(channel.prompts) != 2 {
		t.Fatalf("ask result=%#v err=%v prompts=%d", result, err, len(channel.prompts))
	}
	if !strings.Contains(channel.prompts[0], "danger") || !strings.Contains(channel.prompts[0], "c1") || !strings.Contains(channel.prompts[0], "{\"path\":\"x\"}") {
		t.Fatalf("prompt=%q", channel.prompts[0])
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
	exports, _, err = New(context.Background(), Config{MaxDisplayBytes: 14}, Dependencies{Interaction: sdk.Some[interaction.Channel](channel)})
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

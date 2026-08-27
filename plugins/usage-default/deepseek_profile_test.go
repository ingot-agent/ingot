package usagedefault

import (
	"context"
	"strings"
	"testing"

	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/tool"
	"github.com/ingot-agent/sdk/usage"
)

func TestDeepSeekV4OfficialSimpleVector(t *testing.T) {
	t.Parallel()
	request := model.Request{Messages: []model.Message{
		{Role: model.RoleSystem, Content: "You are a helpful assistant."},
		{Role: model.RoleUser, Content: "Hello"},
	}}
	prompt, err := renderDeepSeekV4Prompt(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	wantPrompt := "<｜begin▁of▁sentence｜>You are a helpful assistant.<｜User｜>Hello<｜Assistant｜><think>"
	if prompt != wantPrompt {
		t.Fatalf("prompt mismatch\ngot:  %q\nwant: %q", prompt, wantPrompt)
	}
	profile, err := newDeepSeekV4Profile()
	if err != nil {
		t.Fatal(err)
	}
	templateCount, err := profile.tokenizer.count(prompt)
	if err != nil {
		t.Fatal(err)
	}
	// Generated with tokenizers 0.21.4 and the official tokenizer.json pinned
	// in assets/README.md.
	if templateCount != 11 {
		encoded, encodeErr := profile.tokenizer.tokenizer.EncodeSingle(prompt, false)
		if encodeErr == nil {
			t.Logf("ids=%v tokens=%q", encoded.GetIds(), encoded.GetTokens())
		}
		t.Fatalf("template count=%d, want 11", templateCount)
	}
	count, err := profile.CountInput(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if count != 90 {
		t.Fatalf("API-default count=%d, want 90", count)
	}
	if profile.Accuracy() != usage.AccuracyEstimate || profile.Source() != deepSeekV4Source {
		t.Fatalf("accuracy=%q source=%q", profile.Accuracy(), profile.Source())
	}
}

func TestDeepSeekV4TokenizerOfficialTextVectors(t *testing.T) {
	t.Parallel()
	tokenizer, err := newDeepSeekV4Tokenizer()
	if err != nil {
		t.Fatal(err)
	}
	vectors := []struct {
		text string
		want int64
	}{
		{text: "Hello! 毕老师！1 + 1 = 2", want: 13},
		{text: "abc1234567 XYZ", want: 5},
		{text: "line1\n\nline2   ", want: 6},
		{text: "你好，世界！", want: 4},
		{text: "foo_bar-baz@example.com", want: 7},
		{text: "  leading and  internal   spaces", want: 7},
	}
	for _, vector := range vectors {
		got, err := tokenizer.count(vector.text)
		if err != nil {
			t.Fatalf("count %q: %v", vector.text, err)
		}
		if got != vector.want {
			encoded, encodeErr := tokenizer.tokenizer.EncodeSingle(vector.text, false)
			if encodeErr == nil {
				t.Logf("tokens for %q: %q", vector.text, encoded.GetTokens())
			}
			t.Errorf("count %q=%d, want %d", vector.text, got, vector.want)
		}
	}
}

func TestDeepSeekV4OfficialToolVector(t *testing.T) {
	t.Parallel()
	request := model.Request{
		Messages: []model.Message{
			{Role: model.RoleSystem, Content: "You are helpful."},
			{Role: model.RoleUser, Content: "Weather?"},
			{Role: model.RoleAssistant, ToolCalls: []tool.Call{{
				ID: "call_a", Name: "weather", Arguments: []byte(`{"city":"北京","days":2}`),
			}}},
			{Role: model.RoleTool, ToolCallID: "call_a", Content: "晴，25°C"},
			{Role: model.RoleUser, Content: "Thanks"},
		},
		Tools: []tool.Definition{{
			Name:        "weather",
			Description: "Get weather",
			InputSchema: []byte(`{"type":"object","properties":{"city":{"type":"string"},"days":{"type":"integer"}},"required":["city"]}`),
		}},
	}
	prompt, err := renderDeepSeekV4Prompt(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		`{"name": "weather", "description": "Get weather", "parameters": {"type": "object"`,
		`<｜DSML｜parameter name="city" string="true">北京</｜DSML｜parameter>`,
		`<tool_result>晴，25°C</tool_result>`,
		"Thanks<｜Assistant｜><think>",
	} {
		if !strings.Contains(prompt, fragment) {
			t.Fatalf("prompt does not contain %q", fragment)
		}
	}
	profile, err := newDeepSeekV4Profile()
	if err != nil {
		t.Fatal(err)
	}
	templateCount, err := profile.tokenizer.count(prompt)
	if err != nil {
		t.Fatal(err)
	}
	// Generated with the official encoding_dsv4.py and tokenizer.json pinned
	// in assets/README.md, using thinking mode and drop_thinking=true.
	if templateCount != 359 {
		t.Fatalf("template count=%d, want 359", templateCount)
	}
	count, err := profile.CountInput(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	// The hosted V4 Flash API reports 438 prompt tokens for this vector with
	// its default thinking/high effort: 359 template + 79 server framing.
	if count != 438 {
		t.Fatalf("API-default count=%d, want 438", count)
	}
}

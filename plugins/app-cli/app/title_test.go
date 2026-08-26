package appcomponent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ingot-agent/sdk/model"
)

func TestTemporaryAndManualSessionTitles(t *testing.T) {
	t.Parallel()
	if got := temporarySessionTitle("  ##  investigate\n   session titles  ", "fallback"); got != "investigate session titles" {
		t.Fatalf("temporary title=%q", got)
	}
	long := strings.Repeat("界", temporaryTitleMaxRunes+10)
	if got := temporarySessionTitle(long, "fallback"); len([]rune(got)) != temporaryTitleMaxRunes || !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated temporary title=%q", got)
	}
	if got := temporarySessionTitle("", "Configured fallback"); got != "Configured fallback" {
		t.Fatalf("fallback title=%q", got)
	}
	if got, err := manualSessionTitle("  Project   X  "); err != nil || got != "Project X" {
		t.Fatalf("manual title=%q err=%v", got, err)
	}
	if _, err := manualSessionTitle(strings.Repeat("x", manualTitleMaxRunes+1)); err == nil {
		t.Fatal("overlong manual title was accepted")
	}
}

func TestGenerateTitleUsesConfiguredModelAndOwnedConversation(t *testing.T) {
	t.Parallel()
	provider := &fakeModel{response: model.Response{Message: model.Message{Role: model.RoleAssistant, Content: "# 标题：会话标题设计。"}}}
	app := &application{model: provider, titleProvider: "cheap", titleModel: "small"}
	title, err := app.generateTitle(context.Background(), "用户问题", "模型回答")
	if err != nil {
		t.Fatal(err)
	}
	if title != "会话标题设计" {
		t.Fatalf("title=%q", title)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("requests=%d", len(provider.requests))
	}
	request := provider.requests[0]
	if request.Provider != "cheap" || request.Model != "small" || len(request.Messages) != 2 || len(request.Tools) != 0 || request.MaxTokens == nil || *request.MaxTokens != automaticTitleMaxTokens {
		t.Fatalf("request=%#v", request)
	}
	var conversation titleConversation
	if err := json.Unmarshal([]byte(request.Messages[1].Content), &conversation); err != nil {
		t.Fatal(err)
	}
	if conversation.User != "用户问题" || conversation.Assistant != "模型回答" {
		t.Fatalf("conversation=%#v", conversation)
	}
}

func TestGeneratedSessionTitleRejectsInvalidShape(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"", "line one\nline two", strings.Repeat("x", automaticTitleMaxRunes+1)} {
		if _, err := generatedSessionTitle(input); err == nil {
			t.Fatalf("generatedSessionTitle(%q) succeeded", input)
		}
	}
}

package interactioncomponent

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	appcli "github.com/ingot-agent/app-cli"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

func testTUIModel() *tuiModel {
	frontend := &tuiFrontend{ready: make(chan struct{}), interrupts: make(chan appcli.Interrupt, 4)}
	return newTUIModel(frontend, normalizedConfig{maxLine: 1024}, "> ")
}

func TestTUIResponsiveSessionLayout(t *testing.T) {
	model := testTUIModel()
	model.applySessionView(appcli.SessionView{
		Current:  "s2",
		Sessions: []session.Summary{{ID: "s1", Title: "First"}, {ID: "s2", Title: "Second"}},
	})
	model.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	if !model.showSidebar || model.mainWidth >= model.width {
		t.Fatalf("wide layout showSidebar=%v main=%d width=%d", model.showSidebar, model.mainWidth, model.width)
	}
	if content := model.View().Content; !strings.Contains(content, "Sessions") || !strings.Contains(content, "Second") {
		t.Fatalf("wide view missing session UI: %q", content)
	}
	model.Update(tea.WindowSizeMsg{Width: 72, Height: 24})
	if model.showSidebar || model.mainWidth != model.width {
		t.Fatalf("narrow layout showSidebar=%v main=%d width=%d", model.showSidebar, model.mainWidth, model.width)
	}
	model.sidebarOpen = true
	if content := model.View().Content; !strings.Contains(content, "Sessions") || !strings.Contains(content, "First") {
		t.Fatalf("overlay missing session UI: %q", content)
	}
}

func TestTUIStreamsMarkdownAndPairsToolResult(t *testing.T) {
	model := testTUIModel()
	if err := model.applyEvent(interaction.TextEvent{Text: "# Result\n\n**hel"}); err != nil {
		t.Fatal(err)
	}
	if err := model.applyEvent(interaction.TextEvent{Text: "lo** and `code`"}); err != nil {
		t.Fatal(err)
	}
	call := tool.Call{ID: "c1", Name: "shell", Arguments: []byte(`{"command":"pwd"}`)}
	if err := model.applyEvent(interaction.ToolCallEvent{Call: call}); err != nil {
		t.Fatal(err)
	}
	if err := model.applyEvent(interaction.ToolResultEvent{Call: call, Result: tool.Result{Content: "/workspace"}}); err != nil {
		t.Fatal(err)
	}
	if len(model.blocks) != 2 || model.blocks[0].text != "# Result\n\n**hello** and `code`" || !model.blocks[1].toolDone {
		t.Fatalf("blocks=%#v", model.blocks)
	}
	model.rebuildTranscript(true)
	content := model.viewport.View()
	for _, want := range []string{"Result", "hello", "code", "Tool", "shell", "/workspace"} {
		if !strings.Contains(content, want) {
			t.Fatalf("transcript missing %q: %q", want, content)
		}
	}
}

func TestTUILoadsHistoryAndOwnsMutableToolCalls(t *testing.T) {
	ui := testTUIModel()
	view := appcli.SessionView{Current: "s1", Messages: []model.Message{
		{Role: model.RoleUser, Content: "question"},
		{Role: model.RoleAssistant, Content: "answer", ToolCalls: []tool.Call{{ID: "c1", Name: "read", Arguments: []byte(`{"path":"a"}`)}}},
		{Role: model.RoleTool, ToolCallID: "c1", Content: "contents"},
	}}
	cloned := cloneSessionView(view)
	view.Messages[1].ToolCalls[0].Arguments[0] = '!'
	ui.applySessionView(cloned)
	if len(ui.blocks) != 3 || !ui.blocks[2].toolDone || string(ui.blocks[2].arguments) != `{"path":"a"}` {
		t.Fatalf("blocks=%#v", ui.blocks)
	}
}

func TestTUIInputAndAskResponses(t *testing.T) {
	model := testTUIModel()
	lineResponse := make(chan inputResult, 1)
	model.request = &requestState{id: 1, response: lineResponse}
	model.composer.SetValue("line one\nline two")
	model.submitLine(model.composer.Value())
	if result := <-lineResponse; result.text != "line one\nline two" {
		t.Fatalf("line response=%#v", result)
	}

	askResponse := make(chan inputResult, 1)
	request := interaction.AskRequest{Prompt: "Approve?", Options: []interaction.AskOption{{Label: "Yes"}, {Label: "No"}}}
	model.request = &requestState{id: 2, response: askResponse, ask: &request, selected: 1}
	model.answer(request.Options[model.request.selected].Label)
	if result := <-askResponse; result.text != "No" {
		t.Fatalf("ask response=%#v", result)
	}
}

func TestTUISanitizesControlSequencesAndTruncatesUTF8(t *testing.T) {
	got := sanitizeText("safe\x1b[31mred\x1b[0m\x00\x07界")
	if got != "safered界" {
		t.Fatalf("sanitizeText()=%q", got)
	}
	value, truncated := truncateUTF8("ab世界", 5)
	if !truncated || value != "ab世" {
		t.Fatalf("truncateUTF8()=(%q, %v)", value, truncated)
	}
	if err := testTUIModel().applyEvent(nil); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("nil event error=%v", err)
	}
}

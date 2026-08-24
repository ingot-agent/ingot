package interactioncomponent

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"

	"github.com/ingot-agent/sdk/interaction"
)

type fakeInput struct {
	mu    sync.Mutex
	lines []string
}

func (f *fakeInput) ReadLine(context.Context, int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.lines) == 0 {
		return "", io.EOF
	}
	line := f.lines[0]
	f.lines = f.lines[1:]
	return line, nil
}
func (f *fakeInput) Close() error { return nil }

func TestChannelSerializesInputAndRendersEvents(t *testing.T) {
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	instance := &channel{
		runCtx: runCtx, cancel: cancel, driver: &fakeInput{lines: []string{"answer", "line"}},
		inputGate: make(chan struct{}, 1), stdout: stdout, stderr: stderr, maxLine: 64, askPrompt: "? ",
	}
	instance.inputGate <- struct{}{}
	answer, err := instance.Ask(context.Background(), interaction.AskRequest{Prompt: "question"})
	if err != nil || answer.Text != "answer" {
		t.Fatalf("answer=%#v err=%v", answer, err)
	}
	line, err := instance.ReadLine(context.Background(), "> ")
	if err != nil || line != "line" {
		t.Fatalf("line=%q err=%v", line, err)
	}
	if err := instance.Render(context.Background(), interaction.TextEvent{Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := instance.Render(context.Background(), interaction.ErrorEvent{Err: io.ErrUnexpectedEOF}); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "question\n? > hello" {
		t.Fatalf("stdout=%q", stdout.String())
	}
	if stderr.String() != "error: unexpected EOF\n" {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestChannelPresentsAndReturnsSelectedOption(t *testing.T) {
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdout := &bytes.Buffer{}
	instance := &channel{
		runCtx: runCtx, cancel: cancel, driver: &fakeInput{lines: []string{"2"}},
		inputGate: make(chan struct{}, 1), stdout: stdout, stderr: &bytes.Buffer{}, maxLine: 64, askPrompt: "? ",
	}
	instance.inputGate <- struct{}{}
	answer, err := instance.Ask(context.Background(), interaction.AskRequest{
		Prompt: "Deploy where?",
		Options: []interaction.AskOption{
			{Label: "Staging", Description: "Verify first"},
			{Label: "Production"},
		},
		AllowTextInput: true,
	})
	if err != nil || answer.Text != "Production" {
		t.Fatalf("answer=%#v err=%v", answer, err)
	}
	want := "Deploy where?\n1. Staging\n   Verify first\n2. Production\n3. Other (enter your own response)\n? "
	if stdout.String() != want {
		t.Fatalf("stdout=%q want=%q", stdout.String(), want)
	}
}

func TestChannelKeepsCustomChoiceInsideOneAsk(t *testing.T) {
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdout := &bytes.Buffer{}
	instance := &channel{
		runCtx: runCtx, cancel: cancel, driver: &fakeInput{lines: []string{"2", "my own answer"}},
		inputGate: make(chan struct{}, 1), stdout: stdout, stderr: &bytes.Buffer{}, maxLine: 64, askPrompt: "? ",
	}
	instance.inputGate <- struct{}{}
	answer, err := instance.Ask(context.Background(), interaction.AskRequest{
		Prompt: "Choose",
		Options: []interaction.AskOption{
			{Label: "Preset"},
		},
		AllowTextInput: true,
	})
	if err != nil || answer.Text != "my own answer" {
		t.Fatalf("answer=%#v err=%v", answer, err)
	}
	want := "Choose\n1. Preset\n2. Other (enter your own response)\n? Enter your response:\n? "
	if stdout.String() != want {
		t.Fatalf("stdout=%q want=%q", stdout.String(), want)
	}
}

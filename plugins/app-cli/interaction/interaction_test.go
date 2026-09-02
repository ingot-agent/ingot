package interactioncomponent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appcli "github.com/ingot-agent/app-cli"
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

type controlledInput struct {
	started chan struct{}
	unblock chan struct{}
	closed  atomic.Int32
}

func (f *controlledInput) ReadLine(ctx context.Context, _ int) (string, error) {
	close(f.started)
	<-f.unblock
	<-ctx.Done()
	return "", ctx.Err()
}

func (f *controlledInput) Close() error {
	f.closed.Add(1)
	return nil
}

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
	answer, err := instance.Request(context.Background(), interaction.Request{Name: "question", Fields: []interaction.Field{{Name: "answer", Label: "question", Kind: interaction.FieldString, Required: true}}})
	if err != nil || len(answer.Values) != 1 || answer.Values[0].Value.String != "answer" {
		t.Fatalf("answer=%#v err=%v", answer, err)
	}
	line, err := instance.ReadLine(context.Background(), "> ")
	if err != nil || line != "line" {
		t.Fatalf("line=%q err=%v", line, err)
	}
	if err := instance.Emit(context.Background(), interaction.Event{Name: "text", Message: "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := instance.Emit(context.Background(), interaction.Event{Name: "error", Level: interaction.LevelError, Message: io.ErrUnexpectedEOF.Error()}); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "question\n? > hello\n" {
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
	answer, err := instance.Request(context.Background(), interaction.Request{
		Name: "deploy", Fields: []interaction.Field{{
			Name: "target", Label: "Deploy where?", Kind: interaction.FieldString, Required: true,
			Options: []interaction.Option{{Value: "staging", Label: "Staging", Description: "Verify first"}, {Value: "production", Label: "Production"}},
		}},
	})
	if err != nil || len(answer.Values) != 1 || answer.Values[0].Value.String != "production" {
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
	answer, err := instance.Request(context.Background(), interaction.Request{
		Name: "choose", Fields: []interaction.Field{{
			Name: "answer", Label: "Choose", Kind: interaction.FieldString, Required: true,
			Options: []interaction.Option{{Value: "preset", Label: "Preset"}},
		}},
	})
	if err != nil || len(answer.Values) != 1 || answer.Values[0].Value.String != "my own answer" {
		t.Fatalf("answer=%#v err=%v", answer, err)
	}
	want := "Choose\n1. Preset\n2. Other (enter your own response)\n? Enter your response:\n? "
	if stdout.String() != want {
		t.Fatalf("stdout=%q want=%q", stdout.String(), want)
	}
}

func TestChannelRejectsInvalidPlainAskPrompt(t *testing.T) {
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	instance := &channel{
		runCtx: runCtx, cancel: cancel, driver: &fakeInput{lines: []string{"unused"}},
		inputGate: make(chan struct{}, 1), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, maxLine: 64, askPrompt: "? ",
	}
	instance.inputGate <- struct{}{}
	_, err := instance.Request(context.Background(), interaction.Request{Name: "ask", Description: string([]byte{0xff}), Fields: []interaction.Field{{Name: "answer", Kind: interaction.FieldString}}})
	if err == nil {
		t.Fatal("Ask() accepted an invalid UTF-8 prompt")
	}
}

func TestChannelRepromptsEmptyCustomChoice(t *testing.T) {
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdout := &bytes.Buffer{}
	instance := &channel{
		runCtx: runCtx, cancel: cancel, driver: &fakeInput{lines: []string{"2", "", "custom"}},
		inputGate: make(chan struct{}, 1), stdout: stdout, stderr: &bytes.Buffer{}, maxLine: 64, askPrompt: "? ",
	}
	instance.inputGate <- struct{}{}
	answer, err := instance.Request(context.Background(), interaction.Request{
		Name: "choose", Fields: []interaction.Field{{Name: "answer", Label: "Choose", Kind: interaction.FieldString, Required: true, Options: []interaction.Option{{Value: "preset", Label: "Preset"}}}},
	})
	if err != nil || len(answer.Values) != 1 || answer.Values[0].Value.String != "custom" {
		t.Fatalf("answer=%#v err=%v", answer, err)
	}
	want := "Choose\n1. Preset\n2. Other (enter your own response)\n? Enter your response:\n? Please enter a response.\n? "
	if stdout.String() != want {
		t.Fatalf("stdout=%q want=%q", stdout.String(), want)
	}
}

func TestChannelCleanupObservesContextAndClosesOnce(t *testing.T) {
	runCtx, cancel := context.WithCancel(context.Background())
	driver := &controlledInput{started: make(chan struct{}), unblock: make(chan struct{})}
	instance := &channel{
		runCtx: runCtx, cancel: cancel, driver: driver,
		inputGate: make(chan struct{}, 1), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, maxLine: 64,
	}
	instance.inputGate <- struct{}{}
	readDone := make(chan error, 1)
	go func() {
		_, err := instance.ReadLine(context.Background(), "> ")
		readDone <- err
	}()
	<-driver.started

	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cleanupCancel()
	if err := instance.cleanup(cleanupCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first cleanup error=%v", err)
	}
	if driver.closed.Load() != 0 {
		t.Fatalf("driver closed while input remained active: %d", driver.closed.Load())
	}
	close(driver.unblock)
	if err := <-readDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadLine() error=%v", err)
	}
	if err := instance.cleanup(context.Background()); err != nil {
		t.Fatalf("second cleanup: %v", err)
	}
	if driver.closed.Load() != 1 {
		t.Fatalf("driver close count=%d", driver.closed.Load())
	}
}

func TestTerminalLeaseIsExclusiveAndReusable(t *testing.T) {
	releaseFirst, ok := acquireTerminalLease()
	if !ok {
		t.Fatal("first terminal lease was unavailable")
	}
	if _, ok := acquireTerminalLease(); ok {
		releaseFirst()
		t.Fatal("second terminal lease unexpectedly succeeded")
	}
	releaseFirst()
	releaseAgain, ok := acquireTerminalLease()
	if !ok {
		t.Fatal("terminal lease was not reusable after release")
	}
	releaseAgain()
}

func TestEmitSerializesCompleteEvents(t *testing.T) {
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdout := &bytes.Buffer{}
	instance := &channel{runCtx: runCtx, cancel: cancel, stdout: stdout, stderr: &bytes.Buffer{}}
	var wait sync.WaitGroup
	for i := 0; i < 20; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := instance.Emit(context.Background(), interaction.Event{Name: "status", Message: "complete"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wait.Wait()
	if got := bytes.Count(stdout.Bytes(), []byte("complete\n")); got != 20 {
		t.Fatalf("complete rendered events=%d output=%q", got, stdout.String())
	}
}

func TestWaitingInputObservesContextCancellation(t *testing.T) {
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	driver := &controlledInput{started: make(chan struct{}), unblock: make(chan struct{})}
	instance := &channel{
		runCtx: runCtx, cancel: cancel, driver: driver,
		inputGate: make(chan struct{}, 1), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, maxLine: 64,
	}
	instance.inputGate <- struct{}{}
	firstDone := make(chan error, 1)
	go func() {
		_, err := instance.ReadLine(context.Background(), "")
		firstDone <- err
	}()
	<-driver.started

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer waitCancel()
	if _, err := instance.ReadLine(waitCtx, ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting ReadLine() error=%v", err)
	}
	cancel()
	close(driver.unblock)
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("first ReadLine() error=%v", err)
	}
}

func TestEmitFormatsSemanticEvents(t *testing.T) {
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdout := &bytes.Buffer{}
	instance := &channel{runCtx: runCtx, cancel: cancel, stdout: stdout, stderr: &bytes.Buffer{}}
	events := []interaction.Event{
		{Name: "ready", Level: interaction.LevelInfo, Message: "ready"},
		{Name: "warning", Level: interaction.LevelWarning, Message: "check this"},
	}
	for _, event := range events {
		if err := instance.Emit(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	want := "ready\ncheck this\n"
	if stdout.String() != want {
		t.Fatalf("stdout=%q want=%q", stdout.String(), want)
	}
}

func TestNormalizeConfigRejectsInvalidValues(t *testing.T) {
	tests := []appcli.InteractionConfig{
		{Color: "sometimes"},
		{MaxLineBytes: -1},
		{AskPrompt: string([]byte{0xff})},
	}
	for i, cfg := range tests {
		if _, err := normalizeConfig(cfg); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("case %d normalizeConfig() error=%v", i, err)
		}
	}
	normalized, err := normalizeConfig(appcli.InteractionConfig{Color: "never"})
	if err != nil {
		t.Fatal(err)
	}
	if normalized.askPrompt != "? " || normalized.maxLine != defaultMaxLineBytes || normalized.color {
		t.Fatalf("normalized=%#v", normalized)
	}
}

//go:build windows

package toolshell

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/workspace"
)

func TestInferDefaultShellPrefersValidComSpec(t *testing.T) {
	root := t.TempDir()
	comspec := filepath.Join(root, "cmd.exe")
	if err := os.WriteFile(comspec, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ComSpec", comspec)
	t.Setenv("SystemRoot", filepath.Join(root, "missing-system-root"))

	got, err := inferDefaultShell()
	if err != nil {
		t.Fatal(err)
	}
	if got != comspec {
		t.Fatalf("inferred shell = %q, want %q", got, comspec)
	}
}

func TestInferDefaultShellRejectsNonCmdComSpec(t *testing.T) {
	root := t.TempDir()
	systemRoot := filepath.Join(root, "system-root")
	fallback := filepath.Join(systemRoot, "System32", "cmd.exe")
	if err := os.MkdirAll(filepath.Dir(fallback), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fallback, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ComSpec", filepath.Join(root, "pwsh.exe"))
	t.Setenv("SystemRoot", systemRoot)

	got, err := inferDefaultShell()
	if err != nil {
		t.Fatal(err)
	}
	if got != fallback {
		t.Fatalf("inferred shell = %q, want fallback %q", got, fallback)
	}
}

func TestInferDefaultShellFailsWhenAllCandidatesAreUnavailable(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ComSpec", filepath.Join(root, "pwsh.exe"))
	t.Setenv("SystemRoot", filepath.Join(root, "missing-system-root"))

	if _, err := inferDefaultShell(); err == nil {
		t.Fatal("inference should fail when no cmd.exe candidate exists")
	}
}

func TestWindowsDecoderNormalizesCP936(t *testing.T) {
	decoder := newWindowsTextDecoder(936)
	input := []byte{0xD6, 0xD0, 0xCE, 0xC4, 0xB2, 0xE2, 0xCA, 0xD4}
	got := append(decoder.Feed(input), decoder.Flush()...)
	if string(got) != "中文测试" {
		t.Fatalf("CP936 output = %q, want 中文测试", got)
	}
	if !utf8.Valid(got) {
		t.Fatalf("CP936 output is not valid UTF-8: %x", got)
	}
}

func TestWindowsDecoderHandlesNativeChunkBoundary(t *testing.T) {
	decoder := newWindowsTextDecoder(936)
	decoder.mode = decoderNative
	if got := decoder.Feed([]byte{0xD6}); len(got) != 0 {
		t.Fatalf("lead byte was emitted early: %x", got)
	}
	got := append(decoder.Feed([]byte{0xD0}), decoder.Flush()...)
	if string(got) != "中" {
		t.Fatalf("chunked CP936 output = %q, want 中", got)
	}
}

func TestWindowsDecoderHandlesLargeInvalidNativeChunk(t *testing.T) {
	decoder := newWindowsTextDecoder(936)
	decoder.mode = decoderNative
	input := make([]byte, 32*1024)
	for i := range input {
		input[i] = 0xFF
	}
	got := append(decoder.Feed(input), decoder.Flush()...)
	if !utf8.Valid(got) {
		t.Fatalf("invalid native output was not normalized: %x", got[:min(len(got), 32)])
	}
	if count := utf8.RuneCount(got); count != len(input) {
		t.Fatalf("replacement rune count = %d, want %d", count, len(input))
	}
}

func TestWindowsDecoderPreservesUTF8(t *testing.T) {
	decoder := newWindowsTextDecoder(936)
	got := append(decoder.Feed([]byte("中文测试")), decoder.Flush()...)
	if string(got) != "中文测试" || !utf8.Valid(got) {
		t.Fatalf("UTF-8 output = %q, valid=%v", got, utf8.Valid(got))
	}
}

func TestWindowsShellChineseOutputIsUTF8(t *testing.T) {
	shell := testShell(t, Config{})
	result, err := invokeShell(t, shell, `echo 中文 & echo 错误 1>&2`)
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(result)
	if !utf8.ValidString(text) {
		t.Fatalf("shell result is not valid UTF-8: %q", text)
	}
	if !strings.Contains(text, "中文") || !strings.Contains(text, "错误") {
		t.Fatalf("shell result lost Chinese output: %q", text)
	}
}

func TestWindowsShellChineseProgressIsText(t *testing.T) {
	consumer := &recordingObservation{}
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Workspace:   staticResolver{binding: workspace.Binding{Root: t.TempDir()}},
		Observation: ingotabi.Some[observation.Consumer](consumer),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := invokeShell(t, exports.Tools[0], `echo 中文 & echo 错误 1>&2`); err != nil {
		t.Fatal(err)
	}
	consumer.mu.Lock()
	defer consumer.mu.Unlock()
	var progressText strings.Builder
	for _, detail := range consumer.details {
		progress, ok := detail.(observation.ToolProgress)
		if !ok {
			t.Fatalf("unexpected observation detail %#v", detail)
		}
		if len(progress.Progress.Content) != 1 || progress.Progress.Content[0].Kind != content.KindText {
			t.Fatalf("Chinese progress was not text: %#v", progress.Progress.Content)
		}
		progressText.WriteString(progress.Progress.Content[0].Text)
	}
	if !utf8.ValidString(progressText.String()) || !strings.Contains(progressText.String(), "中文") || !strings.Contains(progressText.String(), "错误") {
		t.Fatalf("unexpected Chinese progress: %q", progressText.String())
	}
}

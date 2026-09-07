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

func TestInferDefaultShellPrefersPowerShell7(t *testing.T) {
	root := t.TempDir()
	programFiles, systemRoot := setWindowsShellRoots(t, root)
	pwsh := filepath.Join(programFiles, "PowerShell", "7", "pwsh.exe")
	powershell := filepath.Join(systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	cmd := filepath.Join(systemRoot, "System32", "cmd.exe")
	writeTestExecutable(t, pwsh)
	writeTestExecutable(t, powershell)
	writeTestExecutable(t, cmd)

	got, err := inferDefaultShell()
	if err != nil {
		t.Fatal(err)
	}
	if got != pwsh {
		t.Fatalf("inferred shell = %q, want %q", got, pwsh)
	}
}

func TestInferDefaultShellFallsBackToWindowsPowerShell5(t *testing.T) {
	root := t.TempDir()
	_, systemRoot := setWindowsShellRoots(t, root)
	powershell := filepath.Join(systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	cmd := filepath.Join(systemRoot, "System32", "cmd.exe")
	writeTestExecutable(t, powershell)
	writeTestExecutable(t, cmd)

	got, err := inferDefaultShell()
	if err != nil {
		t.Fatal(err)
	}
	if got != powershell {
		t.Fatalf("inferred shell = %q, want %q", got, powershell)
	}
}

func TestInferDefaultShellFallsBackToCmd(t *testing.T) {
	root := t.TempDir()
	_, systemRoot := setWindowsShellRoots(t, root)
	cmd := filepath.Join(systemRoot, "System32", "cmd.exe")
	writeTestExecutable(t, cmd)

	got, err := inferDefaultShell()
	if err != nil {
		t.Fatal(err)
	}
	if got != cmd {
		t.Fatalf("inferred shell = %q, want %q", got, cmd)
	}
}

func TestInferDefaultShellRejectsNonCmdComSpec(t *testing.T) {
	root := t.TempDir()
	_, systemRoot := setWindowsShellRoots(t, root)
	cmd := filepath.Join(systemRoot, "System32", "cmd.exe")
	writeTestExecutable(t, cmd)
	t.Setenv("ComSpec", filepath.Join(root, "custom-shell.exe"))

	got, err := inferDefaultShell()
	if err != nil {
		t.Fatal(err)
	}
	if got != cmd {
		t.Fatalf("inferred shell = %q, want fallback %q", got, cmd)
	}
}

func TestInferDefaultShellFailsWhenAllCandidatesAreUnavailable(t *testing.T) {
	setWindowsShellRoots(t, t.TempDir())
	if _, err := inferDefaultShell(); err == nil {
		t.Fatal("inference should fail when no supported shell exists")
	}
}

func setWindowsShellRoots(t *testing.T, root string) (programFiles, systemRoot string) {
	t.Helper()
	programFiles = filepath.Join(root, "program-files")
	systemRoot = filepath.Join(root, "windows")
	t.Setenv("ProgramW6432", programFiles)
	t.Setenv("ProgramFiles", programFiles)
	t.Setenv("ProgramFiles(x86)", filepath.Join(root, "program-files-x86"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "local-app-data"))
	t.Setenv("SystemRoot", systemRoot)
	t.Setenv("ComSpec", filepath.Join(systemRoot, "System32", "cmd.exe"))
	return programFiles, systemRoot
}

func writeTestExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
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
	command := commandForDefaultShell(t, "", `[Console]::Out.WriteLine('中文'); [Console]::Error.WriteLine('错误')`, `echo 中文 & echo 错误 1>&2`)
	result, err := invokeShell(t, shell, command)
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
	command := commandForDefaultShell(t, "", `[Console]::Out.WriteLine('中文'); [Console]::Error.WriteLine('错误')`, `echo 中文 & echo 错误 1>&2`)
	if _, err := invokeShell(t, exports.Tools[0], command); err != nil {
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

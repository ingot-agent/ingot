package toolshell

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/tool"
)

func testShell(t *testing.T, cfg Config) tool.Tool {
	t.Helper()
	if cfg.WorkingDirectory == "" {
		cfg.WorkingDirectory, _ = os.Getwd()
	}
	if cfg.Shell == "" {
		if runtime.GOOS == "windows" {
			cfg.Shell = os.Getenv("ComSpec")
			if cfg.Shell == "" {
				cfg.Shell = filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
			}
		} else {
			cfg.Shell = "/bin/sh"
		}
	}
	exports, _, err := New(context.Background(), cfg, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	return exports.Tools[0]
}

func TestShellExecReturnsDeterministicEnvelope(t *testing.T) {
	shell := testShell(t, Config{})
	definition := shell.Definition()
	if definition.Name != "shell.exec" || definition.Description != "Execute one command through the configured shell." {
		t.Fatalf("definition = %#v", definition)
	}
	wantSchema := `{"type":"object","additionalProperties":false,"required":["command"],"properties":{"command":{"type":"string","minLength":1},"timeout_seconds":{"type":"integer","minimum":1}}}`
	if string(definition.InputSchema) != wantSchema {
		t.Fatalf("schema = %s, want %s", definition.InputSchema, wantSchema)
	}
	result, err := shell.Invoke(context.Background(), tool.Call{Name: "shell.exec", Arguments: []byte("{\"command\":\"echo hello\"}")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.Content, "exit_code: 0\nstdout:\n") || !strings.Contains(result.Content, "hello") || !strings.Contains(result.Content, "\nstderr:\n") {
		t.Fatalf("unexpected shell result: %q", result.Content)
	}
}

func TestShellOutputLimitAndArgumentValidation(t *testing.T) {
	shell := testShell(t, Config{MaxOutputBytes: 3})
	result, err := shell.Invoke(context.Background(), tool.Call{Arguments: []byte("{\"command\":\"echo hello\"}")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, outputTruncationMarker) {
		t.Fatalf("missing truncation marker: %q", result.Content)
	}
	_, err = shell.Invoke(context.Background(), tool.Call{Arguments: []byte("{\"command\":\"\"}")})
	if !errors.Is(err, ErrInvalidArguments) {
		t.Fatalf("empty command error = %v", err)
	}
}

func TestOutputCollectorUsesFixedPerStreamQuotas(t *testing.T) {
	t.Parallel()

	collector := newOutputCollector(4)
	collector.write(false, []byte("ab"))
	collector.write(true, []byte("12"))
	if got := collector.format(0); strings.Contains(got, outputTruncationMarker) {
		t.Fatalf("exact quotas marked as truncated: %q", got)
	}
	collector.write(false, []byte("c"))
	if got := collector.format(0); !strings.Contains(got, outputTruncationMarker) {
		t.Fatalf("overflow missing truncation marker: %q", got)
	}

	stderrCollector := newOutputCollector(4)
	stderrCollector.write(true, []byte("123"))
	want := "exit_code: 0\nstdout:\n\nstderr:\n12\n" + outputTruncationMarker
	if got := stderrCollector.format(0); got != want {
		t.Fatalf("stderr truncation = %q, want %q", got, want)
	}

	utf8Collector := newOutputCollector(4)
	utf8Collector.write(false, []byte("世"))
	if got := utf8Collector.format(0); !utf8.ValidString(got) {
		t.Fatalf("truncation split UTF-8 output: %q", got)
	}
}

func TestShellTimeoutPreservesContextError(t *testing.T) {
	shell := testShell(t, Config{TimeoutSeconds: 5})
	command := "/bin/sleep 2"
	if runtime.GOOS == "windows" {
		command = `for /L %i in (1,1,100000000) do @rem`
	}
	arguments, err := json.Marshal(map[string]any{"command": command, "timeout_seconds": 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = shell.Invoke(context.Background(), tool.Call{Arguments: arguments})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestShellUsesConfiguredWorkingDirectoryAndIsolatedEnvironment(t *testing.T) {
	const secretKey = "INGOT_TOOL_SHELL_PARENT_SECRET"
	t.Setenv(secretKey, "must-not-leak")
	workingDirectory := t.TempDir()
	command := `pwd; if [ -n "${` + secretKey + `+x}" ]; then printf inherited; else printf isolated; fi`
	if runtime.GOOS == "windows" {
		command = `cd & if defined ` + secretKey + ` (echo inherited) else (echo isolated)`
	}
	shell := testShell(t, Config{WorkingDirectory: workingDirectory})
	result, err := invokeShell(t, shell, command)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Content, "must-not-leak") || strings.Contains(result.Content, "inherited") {
		t.Fatalf("parent environment leaked: %q", result.Content)
	}
	if !strings.Contains(result.Content, "isolated") {
		t.Fatalf("isolation marker missing: %q", result.Content)
	}
	if !strings.Contains(strings.ToLower(result.Content), strings.ToLower(workingDirectory)) {
		t.Fatalf("working directory missing from output: %q", result.Content)
	}
}

func TestShellAllowsOnlyExplicitlyInheritedEnvironment(t *testing.T) {
	const inheritedKey = "INGOT_TOOL_SHELL_ALLOWED"
	t.Setenv(inheritedKey, "allowed-value")
	command := `printf %s "$` + inheritedKey + `"`
	if runtime.GOOS == "windows" {
		command = `echo %` + inheritedKey + `%`
	}
	shell := testShell(t, Config{InheritEnv: []string{inheritedKey}})
	result, err := invokeShell(t, shell, command)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "allowed-value") {
		t.Fatalf("allowlisted environment missing: %q", result.Content)
	}
}

func TestShellReturnsStderrAndNonZeroExitAsResult(t *testing.T) {
	command := `printf problem >&2; exit 7`
	if runtime.GOOS == "windows" {
		command = `echo problem 1>&2 & exit /b 7`
	}
	shell := testShell(t, Config{})
	result, err := invokeShell(t, shell, command)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.Content, "exit_code: 7\n") || !strings.Contains(result.Content, "\nstderr:\nproblem") {
		t.Fatalf("non-zero result = %q", result.Content)
	}
}

func TestEnvironmentKeysAreCaseInsensitiveOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows environment names are case-insensitive")
	}
	_, _, err := New(context.Background(), Config{
		WorkingDirectory: t.TempDir(),
		Shell:            testShellPath(),
		Environment:      map[string]string{"PATH": "one", "Path": "two"},
	}, Dependencies{})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("case-insensitive duplicate error = %v", err)
	}
}

func invokeShell(t *testing.T, shell tool.Tool, command string) (tool.Result, error) {
	t.Helper()
	arguments, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	return shell.Invoke(context.Background(), tool.Call{Name: "shell.exec", Arguments: arguments})
}

func testShellPath() string {
	if runtime.GOOS == "windows" {
		if shell := os.Getenv("ComSpec"); shell != "" {
			return shell
		}
		return filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	}
	return "/bin/sh"
}

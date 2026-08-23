package toolshell

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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
}

func TestShellTimeoutPreservesContextError(t *testing.T) {
	shell := testShell(t, Config{TimeoutSeconds: 5})
	command := "sleep 2"
	if runtime.GOOS == "windows" {
		command = "ping 127.0.0.1 -n 3 >NUL"
	}
	_, err := shell.Invoke(context.Background(), tool.Call{Arguments: []byte("{\"command\":\"" + command + "\",\"timeout_seconds\":1}")})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}
}

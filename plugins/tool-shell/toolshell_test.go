package toolshell

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/execution"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
	"github.com/ingot-agent/sdk/workspace"
)

type recordingObservation struct {
	mu      sync.Mutex
	details []observation.Detail
}

func (r *recordingObservation) Emit(_ context.Context, detail observation.Detail) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.details = append(r.details, detail)
}

// staticResolver resolves every execution scope to one fixed Binding.
type staticResolver struct {
	binding workspace.Binding
	err     error
}

func (r staticResolver) Resolve(context.Context, execution.Scope) (workspace.Binding, error) {
	return r.binding, r.err
}

// scopeResolver resolves each SessionID to its own workspace root.
type scopeResolver struct {
	roots map[session.ID]string
}

func (r scopeResolver) Resolve(_ context.Context, scope execution.Scope) (workspace.Binding, error) {
	root, ok := r.roots[scope.SessionID]
	if !ok {
		return workspace.Binding{}, workspace.ErrNotAssigned
	}
	return workspace.Binding{Root: root}, nil
}

func testShell(t *testing.T, cfg Config) tool.Tool {
	t.Helper()
	return testShellRoot(t, cfg, t.TempDir())
}

func testShellRoot(t *testing.T, cfg Config, root string) tool.Tool {
	t.Helper()
	exports, _, err := New(context.Background(), cfg, Dependencies{Workspace: staticResolver{binding: workspace.Binding{Root: root}}})
	if err != nil {
		t.Fatal(err)
	}
	return exports.Tools[0]
}

// testInvocation builds the runtime envelope for shell_exec under one session.
func testInvocation(name string, arguments []byte) tool.Invocation {
	return tool.Invocation{
		Scope: execution.Scope{SessionID: session.ID("test-session")},
		Call:  tool.Call{Name: name, Arguments: arguments},
	}
}

func TestShellExecReturnsDeterministicEnvelope(t *testing.T) {
	shell := testShell(t, Config{})
	definition := shell.Definition()
	if definition.Name != "shell_exec" || definition.Description != "Execute one command through the configured shell." {
		t.Fatalf("definition = %#v", definition)
	}
	wantSchema := `{"type":"object","additionalProperties":false,"required":["command"],"properties":{"command":{"type":"string","minLength":1},"timeout_seconds":{"type":"integer","minimum":1}}}`
	if string(definition.InputSchema) != wantSchema {
		t.Fatalf("schema = %s, want %s", definition.InputSchema, wantSchema)
	}
	result, err := shell.Invoke(context.Background(), testInvocation("shell_exec", []byte("{\"command\":\"echo hello\"}")))
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(resultText(result)) {
		t.Fatalf("shell result is not valid UTF-8: %q", resultText(result))
	}
	if !strings.HasPrefix(resultText(result), "exit_code: 0\nstdout:\n") || !strings.Contains(resultText(result), "hello") || !strings.Contains(resultText(result), "\nstderr:\n") {
		t.Fatalf("unexpected shell result: %q", resultText(result))
	}
}

func TestShellEmitsStdoutAndStderrProgressOnly(t *testing.T) {
	workingDirectory, _ := os.Getwd()
	consumer := &recordingObservation{}
	exports, _, err := New(context.Background(), Config{
		Shell: testShellPath(),
	}, Dependencies{
		Workspace:   staticResolver{binding: workspace.Binding{Root: workingDirectory}},
		Observation: ingotabi.Some[observation.Consumer](consumer),
	})
	if err != nil {
		t.Fatal(err)
	}
	command := `printf output; printf problem >&2`
	if runtime.GOOS == "windows" {
		command = `echo output & echo problem 1>&2`
	}
	if _, err := invokeShell(t, exports.Tools[0], command); err != nil {
		t.Fatal(err)
	}
	consumer.mu.Lock()
	defer consumer.mu.Unlock()
	channels := map[string]bool{}
	for _, detail := range consumer.details {
		progress, ok := detail.(observation.ToolProgress)
		if !ok {
			t.Fatalf("shell emitted lifecycle detail %#v", detail)
		}
		channels[progress.Progress.Channel] = true
	}
	if !channels["stdout"] || !channels["stderr"] {
		t.Fatalf("progress channels=%v details=%#v", channels, consumer.details)
	}
}

func TestShellOutputLimitAndArgumentValidation(t *testing.T) {
	shell := testShell(t, Config{MaxOutputBytes: 3})
	result, err := shell.Invoke(context.Background(), testInvocation("", []byte("{\"command\":\"echo hello\"}")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resultText(result), outputTruncationMarker) {
		t.Fatalf("missing truncation marker: %q", resultText(result))
	}
	_, err = shell.Invoke(context.Background(), testInvocation("", []byte("{\"command\":\"\"}")))
	if !errors.Is(err, ErrInvalidArguments) {
		t.Fatalf("empty command error = %v", err)
	}
}

func TestOutputCollectorUsesFixedPerStreamQuotas(t *testing.T) {
	t.Parallel()

	collector := newOutputCollector(4)
	collector.writeUTF8(false, []byte("ab"))
	collector.writeUTF8(true, []byte("12"))
	if got := collector.format(0); strings.Contains(got, outputTruncationMarker) {
		t.Fatalf("exact quotas marked as truncated: %q", got)
	}
	collector.writeUTF8(false, []byte("c"))
	if got := collector.format(0); !strings.Contains(got, outputTruncationMarker) {
		t.Fatalf("overflow missing truncation marker: %q", got)
	}

	stderrCollector := newOutputCollector(4)
	stderrCollector.writeUTF8(true, []byte("123"))
	want := "exit_code: 0\nstdout:\n\nstderr:\n12\n" + outputTruncationMarker
	if got := stderrCollector.format(0); got != want {
		t.Fatalf("stderr truncation = %q, want %q", got, want)
	}

	utf8Collector := newOutputCollector(4)
	utf8Collector.writeUTF8(false, []byte("世"))
	if got := utf8Collector.format(0); !utf8.ValidString(got) {
		t.Fatalf("truncation split UTF-8 output: %q", got)
	}

	runeCollector := newOutputCollector(5)
	runeCollector.writeUTF8(false, []byte("世a"))
	runeResult := runeCollector.format(0)
	if !utf8.ValidString(runeResult) || !strings.Contains(runeResult, "世") || !strings.Contains(runeResult, outputTruncationMarker) {
		t.Fatalf("rune-safe truncation = %q", runeResult)
	}

	boundaryCollector := newOutputCollector(4)
	boundaryCollector.writeUTF8(false, []byte("世"))
	boundaryCollector.writeUTF8(false, []byte("a"))
	boundaryResult := boundaryCollector.format(0)
	wantBoundary := "exit_code: 0\nstdout:\n\n" + outputTruncationMarker + "\nstderr:\n"
	if boundaryResult != wantBoundary {
		t.Fatalf("writes after rune-boundary truncation = %q, want %q", boundaryResult, wantBoundary)
	}
}

func TestShellTimeoutReturnsResult(t *testing.T) {
	shell := testShell(t, Config{TimeoutSeconds: 5})
	command := commandForDefaultShell(t, "/bin/sleep 2", "Start-Sleep -Seconds 2", `for /L %i in (1,1,100000000) do @rem`)
	arguments, err := json.Marshal(map[string]any{"command": command, "timeout_seconds": 1})
	if err != nil {
		t.Fatal(err)
	}
	result, err := shell.Invoke(context.Background(), testInvocation("shell_exec", arguments))
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	text := resultText(result)
	if !strings.Contains(text, "exit_code: 124") {
		t.Fatalf("timeout result missing exit_code 124: %q", text)
	}
	if !strings.Contains(text, "timed out after 1s") {
		t.Fatalf("timeout result missing note: %q", text)
	}
}

func TestShellTimeoutHonorsParentCancellation(t *testing.T) {
	shell := testShell(t, Config{TimeoutSeconds: 5})
	command := commandForDefaultShell(t, "/bin/sleep 2", "Start-Sleep -Seconds 2", `for /L %i in (1,1,100000000) do @rem`)
	arguments, err := json.Marshal(map[string]any{"command": command, "timeout_seconds": 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = shell.Invoke(ctx, testInvocation("shell_exec", arguments))
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled parent ctx error = %v, want context.Canceled", err)
	}
}

func TestShellUsesConfiguredWorkingDirectory(t *testing.T) {
	workingDirectory := t.TempDir()
	command := commandForDefaultShell(t, "pwd", "[Console]::Out.Write((Get-Location).Path)", "cd")
	shell := testShellRoot(t, Config{}, workingDirectory)
	result, err := invokeShell(t, shell, command)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(resultText(result)), strings.ToLower(workingDirectory)) {
		t.Fatalf("working directory missing from output: %q", resultText(result))
	}
}

// TestShellInheritsParentEnvironmentByDefault verifies that when inherit_env is
// not configured (the default) the child receives the full parent process
// environment, so user-configured tools and directories (for example the
// user's PATH) are available to commands.
func TestShellInheritsParentEnvironmentByDefault(t *testing.T) {
	const inheritedKey = "INGOT_TOOL_SHELL_DEFAULT_INHERIT"
	t.Setenv(inheritedKey, "inherited-value")
	command := commandForDefaultShell(t, `printf %s "$`+inheritedKey+`"`, `[Console]::Out.Write($env:`+inheritedKey+`)`, `echo %`+inheritedKey+`%`)
	shell := testShell(t, Config{})
	result, err := invokeShell(t, shell, command)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resultText(result), "inherited-value") {
		t.Fatalf("default inheritance missing parent environment: %q", resultText(result))
	}
}

// TestShellExplicitEmptyInheritEnvIsolates verifies that an explicitly empty
// inherit_env list opts into a fully quarantined child environment: parent
// variables must not leak.
func TestShellExplicitEmptyInheritEnvIsolates(t *testing.T) {
	const secretKey = "INGOT_TOOL_SHELL_ISOLATE_SECRET"
	t.Setenv(secretKey, "must-not-leak")
	command := commandForDefaultShell(t,
		`if [ -n "${`+secretKey+`+x}" ]; then printf inherited; else printf isolated; fi`,
		`if (Test-Path Env:`+secretKey+`) { [Console]::Out.Write('inherited') } else { [Console]::Out.Write('isolated') }`,
		`if defined `+secretKey+` (echo inherited) else (echo isolated)`,
	)
	shell := testShell(t, Config{InheritEnv: []string{}})
	result, err := invokeShell(t, shell, command)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(resultText(result), "must-not-leak") || strings.Contains(resultText(result), "inherited") {
		t.Fatalf("explicit empty inherit_env leaked parent environment: %q", resultText(result))
	}
	if !strings.Contains(resultText(result), "isolated") {
		t.Fatalf("isolation marker missing: %q", resultText(result))
	}
}

// TestShellInheritedPWDMatchesWorkspaceRoot verifies that when the child
// inherits the parent environment its PWD is kept consistent with the session
// Workspace Root rather than carrying the parent's stale working directory.
func TestShellInheritedPWDMatchesWorkspaceRoot(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skipf("PWD is not meaningful on %s", runtime.GOOS)
	}
	workingDirectory := t.TempDir()
	shell := testShellRoot(t, Config{}, workingDirectory)
	result, err := invokeShell(t, shell, `printf %s "$PWD"`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resultText(result), workingDirectory) {
		t.Fatalf("inherited PWD does not match workspace root: %q", resultText(result))
	}
}

func TestShellAllowsOnlyExplicitlyInheritedEnvironment(t *testing.T) {
	const inheritedKey = "INGOT_TOOL_SHELL_ALLOWED"
	t.Setenv(inheritedKey, "allowed-value")
	command := commandForDefaultShell(t, `printf %s "$`+inheritedKey+`"`, `[Console]::Out.Write($env:`+inheritedKey+`)`, `echo %`+inheritedKey+`%`)
	shell := testShell(t, Config{InheritEnv: []string{inheritedKey}})
	result, err := invokeShell(t, shell, command)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resultText(result), "allowed-value") {
		t.Fatalf("allowlisted environment missing: %q", resultText(result))
	}
}

func TestShellReturnsStderrAndNonZeroExitAsResult(t *testing.T) {
	command := commandForDefaultShell(t, `printf problem >&2; exit 7`, `[Console]::Error.Write('problem'); exit 7`, `echo problem 1>&2 & exit /b 7`)
	shell := testShell(t, Config{})
	result, err := invokeShell(t, shell, command)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(resultText(result), "exit_code: 7\n") || !strings.Contains(resultText(result), "\nstderr:\nproblem") {
		t.Fatalf("non-zero result = %q", resultText(result))
	}
}

func TestEnvironmentKeysAreCaseInsensitiveOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows environment names are case-insensitive")
	}
	_, _, err := New(context.Background(), Config{
		Shell:       testShellPath(),
		Environment: map[string]string{"PATH": "one", "Path": "two"},
	}, Dependencies{Workspace: staticResolver{binding: workspace.Binding{Root: t.TempDir()}}})
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
	return shell.Invoke(context.Background(), testInvocation("shell_exec", arguments))
}

func resultText(result tool.Result) string {
	value, _ := content.TextOnly(result.Content)
	return value
}

func workingDirectoryCommand() string {
	if runtime.GOOS == "windows" {
		return "cd"
	}
	return "pwd"
}

func commandForDefaultShell(t *testing.T, unixCommand, powershellCommand, cmdCommand string) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		return unixCommand
	}
	shell, err := inferDefaultShell()
	if err != nil {
		t.Fatal(err)
	}
	if isPowerShell(shell) {
		return powershellCommand
	}
	return cmdCommand
}

func TestShellExplicitConfigErrorsDoNotFallback(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-shell")
	if runtime.GOOS == "windows" {
		missing += ".exe"
	}
	if _, _, err := New(context.Background(), Config{Shell: missing}, Dependencies{Workspace: staticResolver{binding: workspace.Binding{Root: t.TempDir()}}}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing explicit shell error = %v", err)
	}
	if _, _, err := New(context.Background(), Config{Shell: "sh"}, Dependencies{Workspace: staticResolver{binding: workspace.Binding{Root: t.TempDir()}}}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("relative explicit shell error = %v", err)
	}
}

func TestFirstUsableShellUsesCandidateOrder(t *testing.T) {
	var checked []string
	got, err := firstUsableShell([]string{"first", "second", "third"}, func(candidate string) bool {
		checked = append(checked, candidate)
		return candidate == "second"
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "second" || strings.Join(checked, ",") != "first,second" {
		t.Fatalf("candidate selection got %q after checking %v", got, checked)
	}
}

func TestFirstUsableShellReportsAllCandidatesWhenUnavailable(t *testing.T) {
	_, err := firstUsableShell([]string{"first", "second"}, func(string) bool { return false })
	if err == nil || !strings.Contains(err.Error(), "[first second]") {
		t.Fatalf("unavailable candidates error = %v", err)
	}
}

func TestShellCommandArgsMatchDialect(t *testing.T) {
	if got := shellCommandArgs(filepath.Join("C:\\", "Program Files", "PowerShell", "7", "pwsh.exe"), "echo hi"); strings.Join(got, "|") != "-Command|echo hi" {
		t.Fatalf("PowerShell arguments = %v", got)
	}
	wantFlag := "-c"
	if runtime.GOOS == "windows" {
		wantFlag = "/C"
	}
	if got := shellCommandArgs(testShellPath(), "echo hi"); strings.Join(got, "|") != wantFlag+"|echo hi" {
		t.Fatalf("platform shell arguments = %v, want flag %s", got, wantFlag)
	}
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

func TestShellResolvesWorkspaceFromInvocationScope(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	resolver := scopeResolver{roots: map[session.ID]string{
		"session-a": rootA,
		"session-b": rootB,
	}}
	cfg := Config{Shell: testShellPath()}
	exports, _, err := New(context.Background(), cfg, Dependencies{Workspace: resolver})
	if err != nil {
		t.Fatal(err)
	}
	shell := exports.Tools[0]
	run := func(scope session.ID) string {
		result, err := shell.Invoke(context.Background(), tool.Invocation{
			Scope: execution.Scope{SessionID: scope},
			Call:  tool.Call{Name: "shell_exec", Arguments: []byte(`{"command":"` + workingDirectoryCommand() + `"}`)},
		})
		if err != nil {
			t.Fatal(err)
		}
		return resultText(result)
	}
	if a, b := run("session-a"), run("session-b"); !strings.Contains(a, rootA) || !strings.Contains(b, rootB) {
		t.Fatalf("session-a pwd=%q session-b pwd=%q", a, b)
	}
}

func TestShellRejectsUnboundAndUnknownSessionScope(t *testing.T) {
	exports, _, err := New(context.Background(), Config{Shell: testShellPath()}, Dependencies{
		Workspace: scopeResolver{roots: map[session.ID]string{"session-a": t.TempDir()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	shell := exports.Tools[0]
	_, err = shell.Invoke(context.Background(), tool.Invocation{
		Scope: execution.Scope{SessionID: "unbound"},
		Call:  tool.Call{Name: "shell_exec", Arguments: []byte(`{"command":"echo hi"}`)},
	})
	if !errors.Is(err, workspace.ErrNotAssigned) {
		t.Fatalf("unbound scope error = %v", err)
	}
	_, err = shell.Invoke(context.Background(), tool.Invocation{
		Scope: execution.Scope{SessionID: "missing-session"},
		Call:  tool.Call{Name: "shell_exec", Arguments: []byte(`{"command":"echo hi"}`)},
	})
	if err == nil {
		t.Fatal("unknown session scope should error")
	}
}

func TestShellNewRejectsMissingWorkspaceResolver(t *testing.T) {
	if _, _, err := New(context.Background(), Config{Shell: testShellPath()}, Dependencies{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing resolver error = %v", err)
	}
}

// TestConcurrentSessionsResolveIndependentWorkspaces runs concurrent shell
// invocations for two Sessions bound to two different Workspaces and asserts
// every command executes in its own session's root. This is the tool-level
// acceptance for the multi-session execution-scope model: no process-cwd,
// global-mutable-workspace, or context.Value source is involved.
func TestConcurrentSessionsResolveIndependentWorkspaces(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	exports, _, err := New(context.Background(), Config{Shell: testShellPath()}, Dependencies{
		Workspace: scopeResolver{roots: map[session.ID]string{"session-a": rootA, "session-b": rootB}},
	})
	if err != nil {
		t.Fatal(err)
	}
	shell := exports.Tools[0]
	invoke := func(scope session.ID) (string, error) {
		result, err := shell.Invoke(context.Background(), tool.Invocation{
			Scope: execution.Scope{SessionID: scope},
			Call:  tool.Call{Name: "shell_exec", Arguments: []byte(`{"command":"` + workingDirectoryCommand() + `"}`)},
		})
		if err != nil {
			return "", err
		}
		return resultText(result), nil
	}
	const rounds = 20
	var wg sync.WaitGroup
	results := make([]string, rounds*2)
	errs := make([]error, rounds*2)
	for i := 0; i < rounds; i++ {
		wg.Add(2)
		go func(i int) { defer wg.Done(); results[2*i], errs[2*i] = invoke("session-a") }(i)
		go func(i int) { defer wg.Done(); results[2*i+1], errs[2*i+1] = invoke("session-b") }(i)
	}
	wg.Wait()
	for i := 0; i < rounds*2; i++ {
		if errs[i] != nil {
			t.Fatalf("round %d: %v", i, errs[i])
		}
		want := rootA
		if i%2 == 1 {
			want = rootB
		}
		if !strings.Contains(results[i], want) {
			t.Fatalf("round %d pwd=%q does not contain root %q", i, results[i], want)
		}
		if results[i] != "" && strings.Contains(results[i], rootA) && strings.Contains(results[i], rootB) {
			t.Fatalf("round %d pwd=%q cross-contaminated", i, results[i])
		}
	}
}

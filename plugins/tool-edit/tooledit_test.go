package tooledit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/execution"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
	"github.com/ingot-agent/sdk/workspace"
)

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

func testTool(t *testing.T, root string, cfg Config) tool.Tool {
	t.Helper()
	exports, _, err := New(context.Background(), cfg, Dependencies{Workspace: staticResolver{binding: workspace.Binding{Root: root}}})
	if err != nil {
		t.Fatal(err)
	}
	return exports.Tools[0]
}

func testInvocation(name string, arguments []byte) tool.Invocation {
	return tool.Invocation{
		Scope: execution.Scope{SessionID: session.ID("test-session")},
		Call:  tool.Call{Name: name, Arguments: arguments},
	}
}

func invoke(t *testing.T, edit tool.Tool, arguments string) tool.Result {
	t.Helper()
	result, err := edit.Invoke(context.Background(), testInvocation(toolName, []byte(arguments)))
	if err != nil {
		t.Fatalf("Invoke error: %v", err)
	}
	return result
}

func resultText(result tool.Result) string {
	value, _ := content.TextOnly(result.Content)
	return value
}

func write(t *testing.T, root, name, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEditDefinitionIsStable(t *testing.T) {
	edit := testTool(t, t.TempDir(), Config{})
	def := edit.Definition()
	if def.Name != toolName || def.Description == "" {
		t.Fatalf("definition = %#v", def)
	}
	want := `{"type":"object","additionalProperties":false,"required":["path","old"],"properties":{"path":{"type":"string","minLength":1},"old":{"type":"string","minLength":1},"new":{"type":"string"},"replace_all":{"type":"boolean"}}}`
	if string(def.InputSchema) != want {
		t.Fatalf("schema = %s, want %s", def.InputSchema, want)
	}
}

func TestEditReplacesFirstOccurrence(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "hello world\n")
	edit := testTool(t, root, Config{})
	result := invoke(t, edit, `{"path":"a.txt","old":"world","new":"ingot"}`)
	if !strings.Contains(resultText(result), "replacements: 1") || !strings.Contains(resultText(result), "edited: a.txt") {
		t.Fatalf("result = %q", resultText(result))
	}
	data, err := os.ReadFile(filepath.Join(root, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello ingot\n" {
		t.Fatalf("file = %q", data)
	}
}

func TestEditReplaceAll(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "x x x\n")
	edit := testTool(t, root, Config{})
	result := invoke(t, edit, `{"path":"a.txt","old":"x","new":"y","replace_all":true}`)
	if !strings.Contains(resultText(result), "replacements: 3") {
		t.Fatalf("result = %q", resultText(result))
	}
	data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(data) != "y y y\n" {
		t.Fatalf("file = %q", data)
	}
}

func TestEditMissingTextIsBusinessResult(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "alpha\n")
	edit := testTool(t, root, Config{})
	result, err := edit.Invoke(context.Background(), testInvocation(toolName, []byte(`{"path":"a.txt","old":"nope","new":"x"}`)))
	if err != nil {
		t.Fatalf("missing text should be a result, got error: %v", err)
	}
	if !strings.Contains(resultText(result), "text not found") {
		t.Fatalf("result = %q", resultText(result))
	}
}

func TestEditMissingFileIsBusinessResult(t *testing.T) {
	root := t.TempDir()
	edit := testTool(t, root, Config{})
	result, err := edit.Invoke(context.Background(), testInvocation(toolName, []byte(`{"path":"absent.txt","old":"x","new":"y"}`)))
	if err != nil {
		t.Fatalf("missing file should be a result, got error: %v", err)
	}
	if !strings.Contains(resultText(result), "file not found") {
		t.Fatalf("result = %q", resultText(result))
	}
}

func TestEditRejectsInvalidArguments(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "alpha\n")
	edit := testTool(t, root, Config{})
	invalid := []string{
		`{}`,
		`{"path":"a.txt"}`,
		`{"path":"a.txt","old":""}`,
		`{"path":"a.txt","old":"alpha","oldx":1}`,
	}
	for _, args := range invalid {
		_, err := edit.Invoke(context.Background(), testInvocation(toolName, []byte(args)))
		if !errors.Is(err, ErrInvalidArguments) {
			t.Fatalf("args=%s error=%v, want ErrInvalidArguments", args, err)
		}
	}
}

func TestEditRejectsAbsoluteAndTraversalPaths(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "x\n")
	edit := testTool(t, root, Config{})
	for _, path := range []string{"/etc/passwd", "../outside.txt", "sub/../../outside.txt"} {
		result, err := edit.Invoke(context.Background(), testInvocation(toolName, []byte(`{"path":"`+path+`","old":"x","new":"y"}`)))
		if err != nil {
			t.Fatalf("path=%s expected a business result, got error: %v", path, err)
		}
		text := resultText(result)
		if !strings.Contains(text, "edit_file error") {
			t.Fatalf("path=%s result = %q, want a business error message", path, text)
		}
	}
}

func TestEditPreservesPermissions(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.sh")
	if err := os.WriteFile(path, []byte("echo hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	edit := testTool(t, root, Config{})
	invoke(t, edit, `{"path":"a.sh","old":"hi","new":"bye"}`)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %o, want 755", info.Mode().Perm())
	}
}

func TestEditRejectsOversizedFile(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", strings.Repeat("a", 20))
	edit := testTool(t, root, Config{MaxFileBytes: 10})
	result, err := edit.Invoke(context.Background(), testInvocation(toolName, []byte(`{"path":"a.txt","old":"a","new":"b"}`)))
	if err != nil {
		t.Fatalf("oversized should be a result, got error: %v", err)
	}
	if !strings.Contains(resultText(result), "exceeds") {
		t.Fatalf("result = %q", resultText(result))
	}
}

func TestEditRejectsNonUTF8File(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "b.txt")
	if err := os.WriteFile(path, []byte{0xff, 0xfe, 0x00}, 0o644); err != nil {
		t.Fatal(err)
	}
	edit := testTool(t, root, Config{})
	result, err := edit.Invoke(context.Background(), testInvocation(toolName, []byte(`{"path":"b.txt","old":"a","new":"b"}`)))
	if err != nil {
		t.Fatalf("non-utf8 should be a result, got error: %v", err)
	}
	if !strings.Contains(resultText(result), "not valid UTF-8") {
		t.Fatalf("result = %q", resultText(result))
	}
}

func TestEditNewRequiresWorkspaceResolver(t *testing.T) {
	if _, _, err := New(context.Background(), Config{}, Dependencies{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing resolver error = %v", err)
	}
}

func TestEditResolvesWorkspaceFromInvocationScope(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	write(t, rootA, "a.txt", "hello A\n")
	write(t, rootB, "a.txt", "hello B\n")
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Workspace: scopeResolver{roots: map[session.ID]string{"session-a": rootA, "session-b": rootB}},
	})
	if err != nil {
		t.Fatal(err)
	}
	edit := exports.Tools[0]
	run := func(scope session.ID) string {
		result, err := edit.Invoke(context.Background(), tool.Invocation{
			Scope: execution.Scope{SessionID: scope},
			Call:  tool.Call{Name: toolName, Arguments: []byte(`{"path":"a.txt","old":"hello","new":"hi"}`)},
		})
		if err != nil {
			t.Fatal(err)
		}
		return resultText(result)
	}
	if !strings.Contains(run("session-a"), "a.txt") || !strings.Contains(run("session-b"), "a.txt") {
		t.Fatal("scope resolution failed")
	}
	if data, _ := os.ReadFile(filepath.Join(rootA, "a.txt")); string(data) != "hi A\n" {
		t.Fatalf("rootA = %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(rootB, "a.txt")); string(data) != "hi B\n" {
		t.Fatalf("rootB = %q", data)
	}
}

func TestEditNoOpWhenOldEqualsNew(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "same\n")
	edit := testTool(t, root, Config{})
	result, err := edit.Invoke(context.Background(), testInvocation(toolName, []byte(`{"path":"a.txt","old":"same","new":"same"}`)))
	if err != nil {
		t.Fatalf("no-op should be a result, got error: %v", err)
	}
	if !strings.Contains(resultText(result), "no-op") {
		t.Fatalf("result = %q", resultText(result))
	}
}

func TestEditCallNameValidation(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "x\n")
	edit := testTool(t, root, Config{})
	_, err := edit.Invoke(context.Background(), testInvocation("other", []byte(`{"path":"a.txt","old":"x","new":"y"}`)))
	if !errors.Is(err, ErrInvalidArguments) {
		t.Fatalf("wrong call name error = %v", err)
	}
}

func TestEditRejectsDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	edit := testTool(t, root, Config{})
	result, err := edit.Invoke(context.Background(), testInvocation(toolName, []byte(`{"path":"dir","old":"x","new":"y"}`)))
	if err != nil {
		t.Fatalf("directory should be a result, got error: %v", err)
	}
	if !strings.Contains(resultText(result), "directory") {
		t.Fatalf("result = %q", resultText(result))
	}
}

func TestEditPreservesParentCancellation(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "x\n")
	edit := testTool(t, root, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := edit.Invoke(ctx, testInvocation(toolName, []byte(`{"path":"a.txt","old":"x","new":"y"}`)))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ctx error = %v, want context.Canceled", err)
	}
}

var _ = json.Marshal

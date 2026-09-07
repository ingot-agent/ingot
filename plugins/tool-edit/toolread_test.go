package tooledit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ingot-agent/sdk/workspace"
)

func TestReadReturnsFileContent(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "hello read\nsecond line\n")
	exports, _, err := New(context.Background(), Config{}, Dependencies{Workspace: staticResolver{binding: workspace.Binding{Root: root}}})
	if err != nil {
		t.Fatal(err)
	}
	read := exports.Tools[1]
	result, err := read.Invoke(context.Background(), testInvocation(readToolName, []byte(`{"path":"a.txt"}`)))
	if err != nil {
		t.Fatal(err)
	}
	if got := resultText(result); got != "hello read\nsecond line\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestReadMissingFileIsBusinessResult(t *testing.T) {
	root := t.TempDir()
	exports, _, err := New(context.Background(), Config{}, Dependencies{Workspace: staticResolver{binding: workspace.Binding{Root: root}}})
	if err != nil {
		t.Fatal(err)
	}
	read := exports.Tools[1]
	result, err := read.Invoke(context.Background(), testInvocation(readToolName, []byte(`{"path":"absent.txt"}`)))
	if err != nil {
		t.Fatalf("missing file should be a result, got error: %v", err)
	}
	if !strings.Contains(resultText(result), "file not found") {
		t.Fatalf("result = %q", resultText(result))
	}
}

func TestReadRejectsAbsoluteAndTraversalPaths(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "x\n")
	exports, _, err := New(context.Background(), Config{}, Dependencies{Workspace: staticResolver{binding: workspace.Binding{Root: root}}})
	if err != nil {
		t.Fatal(err)
	}
	read := exports.Tools[1]
	for _, path := range []string{"/etc/passwd", "../outside.txt"} {
		result, err := read.Invoke(context.Background(), testInvocation(readToolName, []byte(`{"path":"`+path+`"}`)))
		if err != nil {
			t.Fatalf("path=%s expected a business result, got error: %v", path, err)
		}
		if !strings.Contains(resultText(result), "read_file error") {
			t.Fatalf("path=%s result = %q", path, resultText(result))
		}
	}
}

func TestReadRejectsInvalidArguments(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "x\n")
	exports, _, err := New(context.Background(), Config{}, Dependencies{Workspace: staticResolver{binding: workspace.Binding{Root: root}}})
	if err != nil {
		t.Fatal(err)
	}
	read := exports.Tools[1]
	for _, args := range []string{`{}`, `{"path":""}`, `{"path":"a.txt","extra":1}`} {
		_, err := read.Invoke(context.Background(), testInvocation(readToolName, []byte(args)))
		if !errors.Is(err, ErrInvalidArguments) {
			t.Fatalf("args=%s error=%v, want ErrInvalidArguments", args, err)
		}
	}
}

func TestReadRejectsOversizedAndNonUTF8(t *testing.T) {
	root := t.TempDir()
	write(t, root, "big.txt", strings.Repeat("a", 20))
	exports, _, err := New(context.Background(), Config{MaxFileBytes: 10}, Dependencies{Workspace: staticResolver{binding: workspace.Binding{Root: root}}})
	if err != nil {
		t.Fatal(err)
	}
	read := exports.Tools[1]
	result, err := read.Invoke(context.Background(), testInvocation(readToolName, []byte(`{"path":"big.txt"}`)))
	if err != nil {
		t.Fatalf("oversized should be a result, got error: %v", err)
	}
	if !strings.Contains(resultText(result), "exceeds") {
		t.Fatalf("result = %q", resultText(result))
	}

	path := filepath.Join(root, "bin.txt")
	if err := os.WriteFile(path, []byte{0xff, 0x00, 0x01}, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err = read.Invoke(context.Background(), testInvocation(readToolName, []byte(`{"path":"bin.txt"}`)))
	if err != nil {
		t.Fatalf("non-utf8 should be a result, got error: %v", err)
	}
	if !strings.Contains(resultText(result), "not valid UTF-8") {
		t.Fatalf("result = %q", resultText(result))
	}
}

func TestReadDefinitionIsStable(t *testing.T) {
	root := t.TempDir()
	exports, _, err := New(context.Background(), Config{}, Dependencies{Workspace: staticResolver{binding: workspace.Binding{Root: root}}})
	if err != nil {
		t.Fatal(err)
	}
	read := exports.Tools[1]
	def := read.Definition()
	if def.Name != readToolName || def.Description == "" {
		t.Fatalf("definition = %#v", def)
	}
	if want := `{"type":"object","additionalProperties":false,"required":["path"],"properties":{"path":{"type":"string","minLength":1}}}`; string(def.InputSchema) != want {
		t.Fatalf("schema = %s, want %s", def.InputSchema, want)
	}
}

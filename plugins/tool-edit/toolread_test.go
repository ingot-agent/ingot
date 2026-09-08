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
	wantSchema := `{"type":"object","additionalProperties":false,"required":["path"],"properties":{"path":{"type":"string","minLength":1},"start_line":{"type":"integer","minimum":1},"end_line":{"type":"integer","minimum":1}}}`
	if string(def.InputSchema) != wantSchema {
		t.Fatalf("schema = %s, want %s", def.InputSchema, wantSchema)
	}
	if !strings.Contains(def.Description, "start_line") || !strings.Contains(def.Description, "end_line") {
		t.Fatalf("description = %q, want line-range mention", def.Description)
	}
}

func TestReadLineRange(t *testing.T) {
	root := t.TempDir()
	// Five logical lines, no trailing newline.
	write(t, root, "a.txt", "line1\nline2\nline3\nline4\nline5")
	exports, _, err := New(context.Background(), Config{}, Dependencies{Workspace: staticResolver{binding: workspace.Binding{Root: root}}})
	if err != nil {
		t.Fatal(err)
	}
	read := exports.Tools[1]

	cases := []struct {
		name string
		args string
		want string
	}{
		{name: "start only", args: `{"path":"a.txt","start_line":2}`, want: "line2\nline3\nline4\nline5"},
		{name: "end only", args: `{"path":"a.txt","end_line":2}`, want: "line1\nline2"},
		{name: "both inclusive", args: `{"path":"a.txt","start_line":2,"end_line":4}`, want: "line2\nline3\nline4"},
		{name: "single line", args: `{"path":"a.txt","start_line":3,"end_line":3}`, want: "line3"},
		{name: "end clamped", args: `{"path":"a.txt","start_line":4,"end_line":99}`, want: "line4\nline5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := read.Invoke(context.Background(), testInvocation(readToolName, []byte(tc.args)))
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if got := resultText(result); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReadLineRangePreservesTrailingNewline(t *testing.T) {
	root := t.TempDir()
	// Two logical lines with a trailing newline; the final "" fragment must
	// not be counted as a third line.
	write(t, root, "a.txt", "line1\nline2\n")
	exports, _, err := New(context.Background(), Config{}, Dependencies{Workspace: staticResolver{binding: workspace.Binding{Root: root}}})
	if err != nil {
		t.Fatal(err)
	}
	read := exports.Tools[1]
	result, err := read.Invoke(context.Background(), testInvocation(readToolName, []byte(`{"path":"a.txt","start_line":2,"end_line":2}`)))
	if err != nil {
		t.Fatal(err)
	}
	if got := resultText(result); got != "line2" {
		t.Fatalf("got %q, want line2", got)
	}
}

func TestReadLineRangeInvalidIsBusinessResult(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "line1\nline2\nline3")
	exports, _, err := New(context.Background(), Config{}, Dependencies{Workspace: staticResolver{binding: workspace.Binding{Root: root}}})
	if err != nil {
		t.Fatal(err)
	}
	read := exports.Tools[1]
	for _, args := range []string{
		`{"path":"a.txt","start_line":0}`,
		`{"path":"a.txt","end_line":0}`,
		`{"path":"a.txt","start_line":4,"end_line":2}`,
		`{"path":"a.txt","start_line":99}`,
	} {
		result, err := read.Invoke(context.Background(), testInvocation(readToolName, []byte(args)))
		if err != nil {
			t.Fatalf("args=%s expected a business result, got error: %v", args, err)
		}
		if !strings.Contains(resultText(result), "read_file error") {
			t.Fatalf("args=%s result = %q, want business error", args, resultText(result))
		}
	}
}

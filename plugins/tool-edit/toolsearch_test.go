package tooledit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchFindsMatchesInTree(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "hello world\nfoo\n")
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, root, "sub/b.txt", "bar hello\n")
	exports, err := newTestTools(t, root, Config{})
	if err != nil {
		t.Fatal(err)
	}
	search := exports.Tools[2]
	result, err := search.Invoke(context.Background(), testInvocation(searchToolName, []byte(`{"pattern":"hello","path":"."}`)))
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(result)
	if !strings.Contains(text, "2 match(es)") || !strings.Contains(text, "a.txt:1") || !strings.Contains(text, "sub/b.txt:1") {
		t.Fatalf("result = %q", text)
	}
}

func TestSearchDefaultsToWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "needle here\n")
	exports, err := newTestTools(t, root, Config{})
	if err != nil {
		t.Fatal(err)
	}
	search := exports.Tools[2]
	result, err := search.Invoke(context.Background(), testInvocation(searchToolName, []byte(`{"pattern":"needle"}`)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resultText(result), "a.txt:1") {
		t.Fatalf("result = %q", resultText(result))
	}
}

func TestSearchGlobFilters(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "func foo() {}\n")
	write(t, root, "b.txt", "foo\n")
	exports, err := newTestTools(t, root, Config{})
	if err != nil {
		t.Fatal(err)
	}
	search := exports.Tools[2]
	result, err := search.Invoke(context.Background(), testInvocation(searchToolName, []byte(`{"pattern":"foo","path":".","glob":"*.go"}`)))
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(result)
	if !strings.Contains(text, "a.go:1") || strings.Contains(text, "b.txt") {
		t.Fatalf("result = %q", text)
	}
}

func TestSearchNoMatchesIsBusinessResult(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "nothing here\n")
	exports, err := newTestTools(t, root, Config{})
	if err != nil {
		t.Fatal(err)
	}
	search := exports.Tools[2]
	result, err := search.Invoke(context.Background(), testInvocation(searchToolName, []byte(`{"pattern":"zzzz"}`)))
	if err != nil {
		t.Fatalf("no matches should be a result, got error: %v", err)
	}
	if !strings.Contains(resultText(result), "no matches") {
		t.Fatalf("result = %q", resultText(result))
	}
}

func TestSearchSkipsHiddenAndBinaryFiles(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".hidden", "secret here\n")
	write(t, root, "visible.txt", "visible here\n")
	if err := os.WriteFile(filepath.Join(root, "bin.dat"), []byte{0xff, 0x00}, 0o644); err != nil {
		t.Fatal(err)
	}
	exports, err := newTestTools(t, root, Config{})
	if err != nil {
		t.Fatal(err)
	}
	search := exports.Tools[2]
	result, err := search.Invoke(context.Background(), testInvocation(searchToolName, []byte(`{"pattern":"here"}`)))
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(result)
	if strings.Contains(text, ".hidden") || strings.Contains(text, "bin.dat") || !strings.Contains(text, "visible.txt:1") {
		t.Fatalf("result = %q", text)
	}
}

func TestSearchRejectsTraversalPath(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "x\n")
	exports, err := newTestTools(t, root, Config{})
	if err != nil {
		t.Fatal(err)
	}
	search := exports.Tools[2]
	result, err := search.Invoke(context.Background(), testInvocation(searchToolName, []byte(`{"pattern":"x","path":"../outside"}`)))
	if err != nil {
		t.Fatalf("traversal should be a business result, got error: %v", err)
	}
	if !strings.Contains(resultText(result), "search error") {
		t.Fatalf("result = %q", resultText(result))
	}
}

func TestSearchPathNotDirectoryIsBusinessResult(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "x\n")
	exports, err := newTestTools(t, root, Config{})
	if err != nil {
		t.Fatal(err)
	}
	search := exports.Tools[2]
	result, err := search.Invoke(context.Background(), testInvocation(searchToolName, []byte(`{"pattern":"x","path":"a.txt"}`)))
	if err != nil {
		t.Fatalf("file path should be a business result, got error: %v", err)
	}
	if !strings.Contains(resultText(result), "not a directory") {
		t.Fatalf("result = %q", resultText(result))
	}
}

func TestSearchRejectsMissingPattern(t *testing.T) {
	root := t.TempDir()
	exports, err := newTestTools(t, root, Config{})
	if err != nil {
		t.Fatal(err)
	}
	search := exports.Tools[2]
	_, err = search.Invoke(context.Background(), testInvocation(searchToolName, []byte(`{}`)))
	if !errors.Is(err, ErrInvalidArguments) {
		t.Fatalf("missing pattern error = %v, want ErrInvalidArguments", err)
	}
}

func TestSearchDefinitionIsStable(t *testing.T) {
	root := t.TempDir()
	exports, err := newTestTools(t, root, Config{})
	if err != nil {
		t.Fatal(err)
	}
	search := exports.Tools[2]
	def := search.Definition()
	if def.Name != searchToolName || def.Description == "" {
		t.Fatalf("definition = %#v", def)
	}
	if want := `{"type":"object","additionalProperties":false,"required":["pattern"],"properties":{"pattern":{"type":"string","minLength":1},"path":{"type":"string"},"glob":{"type":"string"}}}`; string(def.InputSchema) != want {
		t.Fatalf("schema = %s, want %s", def.InputSchema, want)
	}
}

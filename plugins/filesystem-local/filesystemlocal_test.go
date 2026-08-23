package filesystemlocal_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	filesystemlocal "github.com/ingot-agent/filesystem-local"
	"github.com/ingot-agent/sdk"
	"github.com/ingot-agent/sdk/filesystem"
)

func newFilesystem(t *testing.T) filesystem.FS {
	t.Helper()
	root := t.TempDir()
	exports, cleanup, err := filesystemlocal.New(context.Background(), filesystemlocal.Config{Root: root}, filesystemlocal.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		t.Cleanup(func() { _ = cleanup(context.Background()) })
	}
	return exports.FS
}

func TestComponentContract(t *testing.T) {
	t.Parallel()

	var constructor func(context.Context, filesystemlocal.Config, filesystemlocal.Dependencies) (filesystemlocal.Exports, sdk.Cleanup, error) = filesystemlocal.New
	_ = constructor
	var _ filesystem.FS = newFilesystem(t)
}

func TestWriteReadAndInputOwnership(t *testing.T) {
	t.Parallel()

	workspace := newFilesystem(t)
	input := []byte("original")
	if err := workspace.WriteFile(context.Background(), "file.txt", input, 0o640); err != nil {
		t.Fatal(err)
	}
	input[0] = 'X'
	got, err := workspace.ReadFile(context.Background(), "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Fatalf("content = %q", got)
	}
}

func TestLogicalPathValidation(t *testing.T) {
	t.Parallel()

	workspace := newFilesystem(t)
	invalid := []string{"", "/absolute", "../escape", "a/../b", "./file", "a/./b", "a//b", "a/", `a\b`, "nul\x00byte"}
	if runtime.GOOS == "windows" {
		invalid = append(invalid, "C:/Windows", "file:stream")
	}
	for _, logical := range invalid {
		_, err := workspace.Stat(context.Background(), logical)
		if !errors.Is(err, filesystemlocal.ErrInvalidPath) {
			t.Errorf("Stat(%q) error = %v, want ErrInvalidPath", logical, err)
		}
	}
}

func TestReadDirOrderingAndMkdir(t *testing.T) {
	t.Parallel()

	workspace := newFilesystem(t)
	if err := workspace.MkdirAll(context.Background(), "nested/dir", 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"z.txt", "a.txt", "m.txt"} {
		if err := workspace.WriteFile(context.Background(), "nested/dir/"+name, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := workspace.ReadDir(context.Background(), "nested/dir")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	if want := []string{"a.txt", "m.txt", "z.txt"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %#v, want %#v", names, want)
	}
}

func TestRenameDoesNotReplace(t *testing.T) {
	t.Parallel()

	workspace := newFilesystem(t)
	if err := workspace.WriteFile(context.Background(), "source.txt", []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := workspace.WriteFile(context.Background(), "destination.txt", []byte("destination"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Rename(context.Background(), "source.txt", "destination.txt"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("error = %v, want fs.ErrExist", err)
	}
	got, err := workspace.ReadFile(context.Background(), "destination.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "destination" {
		t.Fatalf("destination = %q", got)
	}
}

func TestRemoveOnlyFileOrEmptyDirectory(t *testing.T) {
	t.Parallel()

	workspace := newFilesystem(t)
	if err := workspace.MkdirAll(context.Background(), "dir", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := workspace.WriteFile(context.Background(), "dir/file", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Remove(context.Background(), "dir"); err == nil {
		t.Fatal("removed non-empty directory")
	}
	if err := workspace.Remove(context.Background(), "dir/file"); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Remove(context.Background(), "dir"); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsSymlink(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	exports, _, err := filesystemlocal.New(context.Background(), filesystemlocal.Config{Root: root}, filesystemlocal.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exports.FS.ReadFile(context.Background(), "link.txt"); !errors.Is(err, filesystemlocal.ErrSymlinkUnsupported) || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("error = %v, want symlink and permission errors", err)
	}
}

func TestCanceledContext(t *testing.T) {
	t.Parallel()

	workspace := newFilesystem(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := workspace.ReadDir(ctx, "."); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestRootReplacementFailsClosed(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	exports, cleanup, err := filesystemlocal.New(context.Background(), filesystemlocal.Config{Root: root}, filesystemlocal.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		t.Cleanup(func() { _ = cleanup(context.Background()) })
	}
	moved := root + "-moved"
	if err := os.Rename(root, moved); err != nil {
		t.Skipf("workspace root cannot be replaced on this platform: %v", err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := exports.FS.Stat(context.Background(), "."); !errors.Is(err, filesystemlocal.ErrRootChanged) {
		t.Fatalf("Stat after root replacement = %v, want ErrRootChanged", err)
	}
}

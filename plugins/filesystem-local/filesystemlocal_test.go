package filesystemlocal_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"

	filesystemlocal "github.com/ingot-agent/filesystem-local"
	"github.com/ingot-agent/ingot-abi"
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

	var constructor func(context.Context, filesystemlocal.Config, filesystemlocal.Dependencies) (filesystemlocal.Exports, ingotabi.Cleanup, error) = filesystemlocal.New
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

func TestRejectsAncestorSymlinkForEveryOperation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "file.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	exports, cleanup, err := filesystemlocal.New(context.Background(), filesystemlocal.Config{Root: root}, filesystemlocal.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		t.Cleanup(func() { _ = cleanup(context.Background()) })
	}
	workspace := exports.FS
	if err := workspace.WriteFile(context.Background(), "source.txt", []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name string
		run  func() error
	}{
		{name: "read", run: func() error { _, err := workspace.ReadFile(context.Background(), "link/file.txt"); return err }},
		{name: "write", run: func() error {
			return workspace.WriteFile(context.Background(), "link/file.txt", []byte("changed"), 0o600)
		}},
		{name: "read directory", run: func() error { _, err := workspace.ReadDir(context.Background(), "link"); return err }},
		{name: "stat", run: func() error { _, err := workspace.Stat(context.Background(), "link/file.txt"); return err }},
		{name: "mkdir", run: func() error { return workspace.MkdirAll(context.Background(), "link/new", 0o700) }},
		{name: "remove", run: func() error { return workspace.Remove(context.Background(), "link/file.txt") }},
		{name: "rename source", run: func() error { return workspace.Rename(context.Background(), "link/file.txt", "renamed.txt") }},
		{name: "rename destination", run: func() error { return workspace.Rename(context.Background(), "source.txt", "link/renamed.txt") }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			err := check.run()
			if !errors.Is(err, filesystemlocal.ErrSymlinkUnsupported) || !errors.Is(err, fs.ErrPermission) {
				t.Fatalf("error = %v, want symlink and permission errors", err)
			}
		})
	}
	contents, err := os.ReadFile(filepath.Join(outside, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "outside" {
		t.Fatalf("outside file changed to %q", contents)
	}
}

func TestConcurrentAncestorReplacementCannotEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not permit the directory replacement pattern used by this test")
	}

	root := t.TempDir()
	outside := t.TempDir()
	active := filepath.Join(root, "active")
	parked := filepath.Join(root, "parked")
	if err := os.Mkdir(active, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(active, "read.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(active, "write.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "read.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "write.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	exports, cleanup, err := filesystemlocal.New(context.Background(), filesystemlocal.Config{Root: root}, filesystemlocal.Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		t.Cleanup(func() { _ = cleanup(context.Background()) })
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := os.Rename(active, parked); err != nil {
				runtime.Gosched()
				continue
			}
			if err := os.Symlink(outside, active); err == nil {
				runtime.Gosched()
				_ = os.Remove(active)
			}
			_ = os.Rename(parked, active)
		}
	}()

	var escaped string
	for range 1_000 {
		contents, readErr := exports.FS.ReadFile(context.Background(), "active/read.txt")
		if readErr == nil && string(contents) != "inside" {
			escaped = "read returned " + string(contents)
			break
		}
		_ = exports.FS.WriteFile(context.Background(), "active/write.txt", []byte("workspace"), 0o600)
		outsideContents, outsideErr := os.ReadFile(filepath.Join(outside, "write.txt"))
		if outsideErr != nil || string(outsideContents) != "outside" {
			escaped = "outside write target changed"
			break
		}
	}
	close(stop)
	<-done
	if escaped != "" {
		t.Fatal(escaped)
	}
}

func TestConcurrentRenameNoReplaceHasSingleWinner(t *testing.T) {
	t.Parallel()

	workspace := newFilesystem(t)
	const contenders = 16
	for index := range contenders {
		name := fmt.Sprintf("source-%02d", index)
		if err := workspace.WriteFile(context.Background(), name, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	errorsByIndex := make([]error, contenders)
	var group sync.WaitGroup
	for index := range contenders {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			source := fmt.Sprintf("source-%02d", index)
			errorsByIndex[index] = workspace.Rename(context.Background(), source, "winner")
		}()
	}
	close(start)
	group.Wait()

	successes := 0
	for _, err := range errorsByIndex {
		if err == nil {
			successes++
			continue
		}
		if errors.Is(err, errors.ErrUnsupported) {
			t.Skipf("atomic no-replace rename is unavailable: %v", err)
		}
		if !errors.Is(err, fs.ErrExist) {
			t.Fatalf("losing rename error = %v, want fs.ErrExist", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful renames = %d, want 1", successes)
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

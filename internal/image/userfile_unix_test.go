//go:build !windows

package image

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestAtomicWriteUserFileHonorsUmaskAndPreservesExistingMode(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "plugins.lock")
	previous := syscall.Umask(0o077)
	defer syscall.Umask(previous)
	if err := AtomicWriteUserFile(path, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("new mode = %o, want 600", got)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWriteUserFile(path, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("replacement mode = %o, want 640", got)
	}
}

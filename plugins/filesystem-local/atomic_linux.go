//go:build linux

package filesystemlocal

import (
	"os"

	"golang.org/x/sys/unix"
)

func atomicReplace(source, destination string) error {
	return os.Rename(source, destination)
}

func atomicRenameNoReplace(source, destination string) error {
	return unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE)
}

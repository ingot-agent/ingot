//go:build linux

package filesystemlocal

import "golang.org/x/sys/unix"

func renameNoReplaceAt(sourceParent int, source string, destinationParent int, destination string) error {
	return unix.Renameat2(sourceParent, source, destinationParent, destination, unix.RENAME_NOREPLACE)
}

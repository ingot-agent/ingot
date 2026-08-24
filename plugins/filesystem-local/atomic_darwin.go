//go:build darwin

package filesystemlocal

import "golang.org/x/sys/unix"

func renameNoReplaceAt(sourceParent int, source string, destinationParent int, destination string) error {
	return unix.RenameatxNp(sourceParent, source, destinationParent, destination, unix.RENAME_EXCL)
}

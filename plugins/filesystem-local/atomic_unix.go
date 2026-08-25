//go:build unix

package filesystemlocal

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"

	"golang.org/x/sys/unix"
)

func atomicReplaceAt(root *os.File, source, destination string) error {
	sourceParent, sourceName, err := openParentAt(root, source)
	if err != nil {
		return err
	}
	defer unix.Close(sourceParent)
	destinationParent, destinationName, err := openParentAt(root, destination)
	if err != nil {
		return err
	}
	defer unix.Close(destinationParent)
	return unix.Renameat(sourceParent, sourceName, destinationParent, destinationName)
}

func atomicRenameNoReplaceAt(root *os.File, source, destination string) error {
	sourceParent, sourceName, err := openParentAt(root, source)
	if err != nil {
		return err
	}
	defer unix.Close(sourceParent)
	destinationParent, destinationName, err := openParentAt(root, destination)
	if err != nil {
		return err
	}
	defer unix.Close(destinationParent)
	if err := renameNoReplaceAt(sourceParent, sourceName, destinationParent, destinationName); err != nil {
		return err
	}
	return nil
}

func openParentAt(root *os.File, logical string) (int, string, error) {
	parent := path.Dir(logical)
	segments, err := logicalSegments(parent)
	if err != nil {
		return -1, "", err
	}
	current, err := unix.Dup(int(root.Fd()))
	if err != nil {
		return -1, "", err
	}
	for _, segment := range segments {
		next, openErr := unix.Openat(current, segment, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(current)
		if openErr != nil {
			if errors.Is(openErr, unix.ELOOP) {
				return -1, "", fmt.Errorf("%w: %w", ErrSymlinkUnsupported, fs.ErrPermission)
			}
			return -1, "", openErr
		}
		current = next
	}
	return current, path.Base(logical), nil
}

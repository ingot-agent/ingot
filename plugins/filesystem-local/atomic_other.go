//go:build unix && !linux && !darwin

package filesystemlocal

import (
	"errors"
	"fmt"
	"io/fs"

	"golang.org/x/sys/unix"
)

func renameNoReplaceAt(_ int, _ string, destinationParent int, destination string) error {
	var info unix.Stat_t
	if err := unix.Fstatat(destinationParent, destination, &info, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		return fmt.Errorf("destination exists: %w", fs.ErrExist)
	} else if !errors.Is(err, unix.ENOENT) {
		return err
	}
	return fmt.Errorf("atomic no-replace rename is unavailable on this platform: %w", errors.ErrUnsupported)
}

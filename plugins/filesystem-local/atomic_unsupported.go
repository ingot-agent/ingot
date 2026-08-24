//go:build !unix && !windows

package filesystemlocal

import (
	"errors"
	"fmt"
	"os"
)

func atomicReplaceAt(_ *os.File, _, _ string) error {
	return fmt.Errorf("atomic replace is unavailable on this platform: %w", errors.ErrUnsupported)
}

func atomicRenameNoReplaceAt(_ *os.File, _, _ string) error {
	return fmt.Errorf("atomic no-replace rename is unavailable on this platform: %w", errors.ErrUnsupported)
}

//go:build !windows && !linux && !darwin

package filesystemlocal

import (
	"fmt"
	"io/fs"
	"os"
)

func atomicReplace(source, destination string) error {
	return os.Rename(source, destination)
}

func atomicRenameNoReplace(source, destination string) error {
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("destination exists: %w", fs.ErrExist)
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(source, destination)
}

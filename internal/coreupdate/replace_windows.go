//go:build windows

package coreupdate

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func replaceExecutable(candidate, destination string) error {
	backup := destination + ".old"
	if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := moveFile(destination, backup, true); err != nil {
		return err
	}
	if err := moveFile(candidate, destination, false); err != nil {
		return errors.Join(err, moveFile(backup, destination, true))
	}
	return nil
}

func cleanupPreviousExecutable(destination string) error {
	err := os.Remove(destination + ".old")
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func moveFile(source, destination string, replace bool) error {
	sourcePath, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPath, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	flags := uint32(windows.MOVEFILE_WRITE_THROUGH)
	if replace {
		flags |= windows.MOVEFILE_REPLACE_EXISTING
	}
	return windows.MoveFileEx(sourcePath, destinationPath, flags)
}

func syncFile(path string) error {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func syncDirectory(string) error { return nil }

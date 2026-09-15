//go:build !windows

package coreupdate

import (
	"os"
	"path/filepath"
)

func replaceExecutable(candidate, destination string) error {
	if err := os.Rename(candidate, destination); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(destination))
}

func cleanupPreviousExecutable(string) error { return nil }

func syncFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

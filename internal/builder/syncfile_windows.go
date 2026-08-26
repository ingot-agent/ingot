//go:build windows

package builder

import "os"

func syncFile(path string) error {
	// FlushFileBuffers requires a handle opened for writing on Windows.
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	return file.Sync()
}

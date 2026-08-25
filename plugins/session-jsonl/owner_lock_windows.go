//go:build windows

package sessionjsonl

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func acquireOwnerLock(path string) (func() error, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	var overlapped windows.Overlapped
	err = windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
	if err != nil {
		_ = file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrStateDirLocked
		}
		return nil, fmt.Errorf("lock state directory: %w", err)
	}
	return func() error {
		unlockErr := windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
		closeErr := file.Close()
		return errors.Join(unlockErr, closeErr)
	}, nil
}

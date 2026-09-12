//go:build windows

package process

import (
	"context"
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"time"
)

var fmtLockHeld = errors.New("lock is held")

func acquireFileLock(ctx context.Context, path string, wait bool) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	var overlapped windows.Overlapped
	for {
		err = windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
		if err == nil {
			return func() { _ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped); _ = file.Close() }, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			file.Close()
			return nil, err
		}
		if !wait {
			file.Close()
			return nil, fmtLockHeld
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}
func lockHeld(path string) (bool, error) {
	release, err := acquireFileLock(context.Background(), path, false)
	if errors.Is(err, fmtLockHeld) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	release()
	return false, nil
}

func LocksHeld(runtimeHome string) (bool, bool, error) {
	supervisor, err := lockHeld(SupervisorLockPath(runtimeHome))
	if err != nil {
		return false, false, err
	}
	writer, err := lockHeld(WriterLockPath(runtimeHome))
	return supervisor, writer, err
}

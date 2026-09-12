//go:build !windows

package process

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

func acquireFileLock(ctx context.Context, path string, wait bool) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
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

var fmtLockHeld = errors.New("lock is held")

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

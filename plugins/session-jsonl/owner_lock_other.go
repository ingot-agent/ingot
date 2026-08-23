//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package sessionjsonl

func acquireOwnerLock(string) (func() error, error) {
	return nil, ErrOwnerLockUnsupported
}

//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package sessionjsonl

func syncDirectory(string) error { return nil }

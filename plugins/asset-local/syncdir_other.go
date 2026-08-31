//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package assetlocal

func syncDirectory(string) error { return nil }

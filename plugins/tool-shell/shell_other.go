//go:build !windows && !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package toolshell

import "fmt"

func inferDefaultShell() (string, error) {
	return "", fmt.Errorf("automatic shell resolution is unsupported on this platform")
}

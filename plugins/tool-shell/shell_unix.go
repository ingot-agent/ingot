//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package toolshell

func inferDefaultShell() (string, error) {
	return firstUsableShell([]string{"/bin/sh", "/usr/bin/sh"}, usableShell)
}

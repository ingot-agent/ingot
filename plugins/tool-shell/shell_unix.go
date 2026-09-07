//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package toolshell

import (
	"fmt"
	"os"
	"os/user"
	"strings"
)

func inferDefaultShell() (string, error) {
	var userShell string
	if shell, err := currentUserShell(); err == nil && shell != "" {
		userShell = shell
	}
	return firstUsableShell(unixDefaultShellCandidates(userShell), usableShell)
}

func unixDefaultShellCandidates(userShell string) []string {
	candidates := make([]string, 0, 4)
	if userShell != "" {
		candidates = append(candidates, userShell)
	}
	return append(candidates, "/usr/bin/zsh", "/usr/bin/sh", "/bin/sh")
}

// os/user.User does not expose the login shell. Use Current to identify the
// account, then read the shell field from the Unix user database.
func currentUserShell() (string, error) {
	current, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("resolve current user: %w", err)
	}
	passwd, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return "", fmt.Errorf("read Unix user database: %w", err)
	}
	if shell := shellFromPasswd(string(passwd), current.Uid, current.Username); shell != "" {
		return shell, nil
	}
	return "", fmt.Errorf("current user %q has no shell entry", current.Username)
}

func shellFromPasswd(passwd, uid, username string) string {
	var usernameMatch string
	for _, line := range strings.Split(passwd, "\n") {
		fields := strings.Split(line, ":")
		if len(fields) < 7 {
			continue
		}
		shell := strings.TrimSpace(fields[6])
		if shell == "" {
			continue
		}
		if uid != "" && fields[2] == uid {
			return shell
		}
		if usernameMatch == "" && username != "" && fields[0] == username {
			usernameMatch = shell
		}
	}
	return usernameMatch
}

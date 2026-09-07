//go:build windows

package toolshell

import (
	"os"
	"path/filepath"
	"strings"
)

func inferDefaultShell() (string, error) {
	var candidates []string
	if comspec := os.Getenv("ComSpec"); filepath.IsAbs(comspec) && strings.EqualFold(filepath.Base(comspec), "cmd.exe") {
		candidates = append(candidates, comspec)
	}
	if root := os.Getenv("SystemRoot"); root != "" {
		candidates = append(candidates, filepath.Join(root, "System32", "cmd.exe"))
	}
	return firstUsableShell(candidates, usableShell)
}

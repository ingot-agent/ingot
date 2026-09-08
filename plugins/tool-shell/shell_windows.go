//go:build windows

package toolshell

import (
	"os"
	"path/filepath"
	"strings"
)

func inferDefaultShell() (string, error) {
	return firstUsableShell(windowsDefaultShellCandidates(), usableShell)
}

func windowsDefaultShellCandidates() []string {
	var candidates []string
	appendCandidate := func(candidate string) {
		if !filepath.IsAbs(candidate) {
			return
		}
		for _, existing := range candidates {
			if strings.EqualFold(existing, candidate) {
				return
			}
		}
		candidates = append(candidates, candidate)
	}

	// Windows has no login-shell field equivalent to Unix /etc/passwd. Prefer
	// PowerShell 7, then Windows PowerShell 5, and finally cmd.exe.
	for _, root := range []string{os.Getenv("ProgramW6432"), os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")} {
		if root != "" {
			appendCandidate(filepath.Join(root, "PowerShell", "7", "pwsh.exe"))
		}
	}
	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		appendCandidate(filepath.Join(localAppData, "Microsoft", "WindowsApps", "pwsh.exe"))
	}

	systemRoot := os.Getenv("SystemRoot")
	if systemRoot != "" {
		appendCandidate(filepath.Join(systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe"))
	}

	if comspec := os.Getenv("ComSpec"); filepath.IsAbs(comspec) && strings.EqualFold(filepath.Base(comspec), "cmd.exe") {
		appendCandidate(comspec)
	}
	if systemRoot != "" {
		appendCandidate(filepath.Join(systemRoot, "System32", "cmd.exe"))
	}
	return candidates
}

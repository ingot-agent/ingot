package toolshell

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func resolveShell(configured string) (string, error) {
	if configured != "" {
		if !filepath.IsAbs(configured) {
			return "", fmt.Errorf("shell must be an absolute executable path: %w", ErrInvalidConfig)
		}
		shell, err := validateShell(configured)
		if err != nil {
			return "", err
		}
		return shell, nil
	}

	inferred, err := inferDefaultShell()
	if err != nil {
		return "", fmt.Errorf("infer default shell: %w: %w", ErrInvalidConfig, err)
	}
	shell, err := validateShell(inferred)
	if err != nil {
		return "", err
	}
	return shell, nil
}

func validateShell(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("shell must be an absolute executable path: %w", ErrInvalidConfig)
	}
	shell, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve shell: %w: %w", ErrInvalidConfig, err)
	}
	shellInfo, err := os.Stat(shell)
	if err != nil {
		return "", fmt.Errorf("stat shell %q: %w: %w", shell, ErrInvalidConfig, err)
	}
	if shellInfo.IsDir() || (runtime.GOOS != "windows" && shellInfo.Mode()&0o111 == 0) {
		return "", fmt.Errorf("shell %q is not executable: %w", shell, ErrInvalidConfig)
	}
	return shell, nil
}

func usableShell(path string) bool {
	_, err := validateShell(path)
	return err == nil
}

func firstUsableShell(candidates []string, usable func(string) bool) (string, error) {
	for _, candidate := range candidates {
		if usable(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no usable default shell found; tried: %v", candidates)
}

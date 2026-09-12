//go:build !linux && !windows

package process

import (
	"fmt"
	"os"
)

func BirthIdentity(pid int) (string, error) {
	process, err := os.FindProcess(pid)
	if err != nil {
		return "", err
	}
	if err := process.Signal(os.Signal(nil)); err != nil {
		return "", err
	}
	return fmt.Sprintf("pid:%d", pid), nil
}
func IdentityAlive(pid int, birth string) bool {
	current, err := BirthIdentity(pid)
	return err == nil && current == birth
}

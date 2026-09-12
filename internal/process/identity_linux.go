//go:build linux

package process

import (
	"fmt"
	"os"
	"strings"
)

func BirthIdentity(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return "", fmt.Errorf("invalid proc stat")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) <= 19 {
		return "", fmt.Errorf("short proc stat")
	}
	return fields[19], nil
}
func IdentityAlive(pid int, birth string) bool {
	current, err := BirthIdentity(pid)
	return err == nil && current == birth
}

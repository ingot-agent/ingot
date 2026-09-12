//go:build windows

package process

import (
	"fmt"
	"golang.org/x/sys/windows"
)

func BirthIdentity(pid int) (string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d", creation.Nanoseconds()), nil
}
func IdentityAlive(pid int, birth string) bool {
	current, err := BirthIdentity(pid)
	return err == nil && current == birth
}

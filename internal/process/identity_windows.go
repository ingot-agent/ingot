//go:build windows

package process

import (
	"fmt"

	"golang.org/x/sys/windows"
)

const processIdentityAccess = windows.PROCESS_QUERY_LIMITED_INFORMATION | windows.SYNCHRONIZE

func BirthIdentity(pid int) (string, error) {
	handle, err := windows.OpenProcess(processIdentityAccess, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	identity, alive, err := processIdentity(handle)
	if err != nil {
		return "", err
	}
	if !alive {
		return "", fmt.Errorf("process %d is not active", pid)
	}
	return identity, nil
}

func IdentityAlive(pid int, birth string) bool {
	handle, err := windows.OpenProcess(processIdentityAccess, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	current, alive, err := processIdentity(handle)
	return err == nil && alive && current == birth
}

func processIdentity(handle windows.Handle) (string, bool, error) {
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return "", false, err
	}
	waitStatus, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return "", false, err
	}
	switch waitStatus {
	case windows.WAIT_OBJECT_0:
		return fmt.Sprintf("%d", creation.Nanoseconds()), false, nil
	case uint32(windows.WAIT_TIMEOUT):
		return fmt.Sprintf("%d", creation.Nanoseconds()), true, nil
	default:
		return "", false, fmt.Errorf("unexpected process wait status %d", waitStatus)
	}
}

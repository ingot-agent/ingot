//go:build windows

package process

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// GetExitCodeProcess reports STILL_ACTIVE using the NT status value for pending.
const stillActiveProcessExitCode = uint32(windows.STATUS_PENDING)

func BirthIdentity(pid int) (string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
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
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
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
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return "", false, err
	}
	return fmt.Sprintf("%d", creation.Nanoseconds()), exitCode == stillActiveProcessExitCode, nil
}

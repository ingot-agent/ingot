//go:build windows

package home

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func configureDetached(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

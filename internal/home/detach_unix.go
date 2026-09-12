//go:build !windows

package home

import (
	"os/exec"
	"syscall"
)

func configureDetached(command *exec.Cmd){command.SysProcAttr=&syscall.SysProcAttr{Setsid:true}}

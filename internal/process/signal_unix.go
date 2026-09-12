//go:build !windows

package process

import (
	"os"
	"os/exec"
	"syscall"
)

func terminate(process *os.Process) error { return process.Signal(syscall.SIGTERM) }
func configureRuntimeProcess(*exec.Cmd)   {}

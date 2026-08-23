//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package toolshell

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

type processController struct{ pid int }

func newProcessController(command *exec.Cmd) (processController, error) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return processController{}, nil
}

func (c *processController) Attach(process *os.Process) error { c.pid = process.Pid; return nil }

func (c *processController) Terminate(process *os.Process) error {
	if c.pid == 0 {
		return process.Kill()
	}
	if err := syscall.Kill(-c.pid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		return err
	}
	time.Sleep(250 * time.Millisecond)
	if err := syscall.Kill(-c.pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}

func (c *processController) Close() error { return nil }

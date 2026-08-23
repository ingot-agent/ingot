//go:build !windows && !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package toolshell

import (
	"os"
	"os/exec"
)

type processController struct{}

func newProcessController(_ *exec.Cmd) (processController, error) { return processController{}, nil }
func (c *processController) Attach(_ *os.Process) error           { return nil }
func (c *processController) Terminate(process *os.Process) error  { return process.Kill() }
func (c *processController) Close() error                         { return nil }

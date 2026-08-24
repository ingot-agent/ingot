//go:build windows

package script

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type processController struct{ job windows.Handle }

func newProcessController(command *exec.Cmd) (processController, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return processController{}, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(job)
		return processController{}, err
	}
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED}
	return processController{job: job}, nil
}

func (c *processController) Attach(process *os.Process) error {
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(process.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	if err := windows.AssignProcessToJobObject(c.job, handle); err != nil {
		return err
	}
	return resumePrimaryThread(uint32(process.Pid))
}

func (c *processController) Terminate(process *os.Process) error {
	_ = windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(process.Pid))
	time.Sleep(250 * time.Millisecond)
	return windows.TerminateJobObject(c.job, 1)
}

func (c *processController) Close() error {
	if c.job == 0 {
		return nil
	}
	err := windows.CloseHandle(c.job)
	c.job = 0
	return err
}

func resumePrimaryThread(processID uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("snapshot process threads: %w", err)
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		return err
	}
	for {
		if entry.OwnerProcessID == processID {
			thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if err != nil {
				return err
			}
			_, resumeErr := windows.ResumeThread(thread)
			closeErr := windows.CloseHandle(thread)
			if resumeErr != nil {
				return resumeErr
			}
			return closeErr
		}
		entry.Size = uint32(unsafe.Sizeof(windows.ThreadEntry32{}))
		if err := windows.Thread32Next(snapshot, &entry); err != nil {
			return fmt.Errorf("find primary process thread for pid %d: %w", processID, err)
		}
	}
}

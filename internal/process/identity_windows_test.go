//go:build windows

package process

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestIdentityAliveRejectsExitedProcessWithRetainedHandle(t *testing.T) {
	if os.Getenv("INGOT_IDENTITY_HELPER") == "1" {
		fmt.Fprintln(os.Stdout, "ready")
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}

	command := exec.Command(os.Args[0], "-test.run=TestIdentityAliveRejectsExitedProcessWithRetainedHandle")
	command.Env = append(os.Environ(), "INGOT_IDENTITY_HELPER=1")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if waited {
			return
		}
		_ = stdin.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
	}()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("helper readiness = %q, %v", line, err)
	}

	birth, err := BirthIdentity(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(command.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	waited = true

	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		t.Fatal(err)
	}
	if exitCode == stillActiveProcessExitCode {
		t.Fatal("helper process is still active")
	}
	if IdentityAlive(command.Process.Pid, birth) {
		t.Fatal("exited process was reported as alive")
	}
}

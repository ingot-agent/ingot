//go:build windows

package process

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestIdentityAliveRejectsExitedProcessWithRetainedHandle(t *testing.T) {
	testIdentityAliveRejectsExitedProcess(t, 0)
}

func TestIdentityAliveRejectsExitedProcessWithStillActiveExitCode(t *testing.T) {
	testIdentityAliveRejectsExitedProcess(t, int(windows.STATUS_PENDING))
}

func testIdentityAliveRejectsExitedProcess(t *testing.T, exitCode int) {
	if helperExitCode := os.Getenv("INGOT_IDENTITY_HELPER_EXIT"); helperExitCode != "" {
		code, err := strconv.Atoi(helperExitCode)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(os.Stdout, "ready")
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(code)
	}

	command := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	command.Env = append(os.Environ(), "INGOT_IDENTITY_HELPER_EXIT="+strconv.Itoa(exitCode))
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
	handle, err := windows.OpenProcess(processIdentityAccess, false, uint32(command.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	waitErr := command.Wait()
	waited = true
	if exitCode == 0 {
		if waitErr != nil {
			t.Fatal(waitErr)
		}
	} else {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) || exitErr.ExitCode() != exitCode {
			t.Fatalf("helper exit = %v, want %d", waitErr, exitCode)
		}
	}

	waitStatus, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		t.Fatal(err)
	}
	if waitStatus != windows.WAIT_OBJECT_0 {
		t.Fatalf("helper wait status = %d, want WAIT_OBJECT_0", waitStatus)
	}
	if IdentityAlive(command.Process.Pid, birth) {
		t.Fatal("exited process was reported as alive")
	}
}

//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package toolshell

import (
	"reflect"
	"testing"
)

func TestUnixDefaultShellCandidatesPreferUserShell(t *testing.T) {
	want := []string{"/opt/custom-shell", "/usr/bin/zsh", "/usr/bin/sh", "/bin/sh"}
	if got := unixDefaultShellCandidates("/opt/custom-shell"); !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

func TestShellFromPasswdUsesCurrentUID(t *testing.T) {
	passwd := "alice:x:1000:1000:Alice:/home/alice:/bin/bash\n" +
		"alice:x:2000:2000:Other:/home/other:/usr/bin/zsh\n"
	if got := shellFromPasswd(passwd, "2000", "alice"); got != "/usr/bin/zsh" {
		t.Fatalf("shell = %q, want /usr/bin/zsh", got)
	}
}

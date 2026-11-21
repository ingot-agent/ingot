package builder

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNormalizeGoVersion(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		input string
		want  string
		ok    bool
	}{
		{input: "go1.24.2", want: "go1.24.2", ok: true},
		{input: "1.24.2", want: "go1.24.2", ok: true},
		{input: " go1.24.2\n", want: "go1.24.2", ok: true},
		{input: "1.24", want: "go1.24.0", ok: true},
		{input: "go1.25", want: "go1.25.0", ok: true},
		{input: "1.24.10", want: "go1.24.10", ok: true},
		{input: "", ok: false},
		{input: "go1.25rc1", ok: false},
		{input: "go1.25beta1", ok: false},
		{input: "devel go1.27-abcdef", ok: false},
		{input: "go1.24.2 X:boringcrypto", ok: false},
		{input: "go2.0.0", ok: false},
		{input: "v1.24.2", ok: false},
	} {
		got, ok := normalizeGoVersion(test.input)
		if ok != test.ok || (ok && got != test.want) {
			t.Fatalf("normalizeGoVersion(%q) = %q, %v; want %q, %v", test.input, got, ok, test.want, test.ok)
		}
	}
}

func TestCompareGoVersions(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		left, right string
		want        int
	}{
		{"go1.24.0", "go1.24.0", 0},
		{"go1.24.1", "go1.24.0", 1},
		{"go1.24.0", "go1.25.0", -1},
		{"go1.25.0", "go1.24.9", 1},
		{"go1.9.0", "go1.10.0", -1},
	} {
		if got := compareGoVersions(test.left, test.right); got != test.want {
			t.Fatalf("compareGoVersions(%q, %q) = %d; want %d", test.left, test.right, got, test.want)
		}
	}
}

func TestRequiredGoVersionSelectsHighestModuleDirective(t *testing.T) {
	t.Parallel()
	modules := []LockedModule{
		{Path: "example.com/a", Version: "v1.0.0", GoVersion: "1.21.0"},
		{Path: "example.com/b", Version: "v2.0.0", GoVersion: "1.25.0"},
		{Path: "example.com/c", Version: "v3.0.0", GoVersion: "1.24.2"},
	}
	requirement, recorded := requiredGoVersion(modules)
	if !recorded || requirement.Version != "go1.25.0" || requirement.Source != "example.com/b@v2.0.0" {
		t.Fatalf("requirement = %#v recorded=%v", requirement, recorded)
	}
	floor, recorded := requiredGoVersion(nil)
	if recorded || floor.Version != MinimumGeneratedGoVersion || floor.Source != "" {
		t.Fatalf("floor = %#v recorded=%v", floor, recorded)
	}
}

func TestLockDerivesMinimumGoVersionFromModules(t *testing.T) {
	t.Parallel()
	lock := fixtureGraphLock("/machine/provider-a", "/machine/provider-b", "/machine/consumer")
	lock.MinimumGoVersion = ""
	if err := lock.Validate(); err != nil {
		t.Fatal(err)
	}
	if lock.MinimumGoVersion != "go1.24.0" {
		t.Fatalf("derived minimum = %q, want go1.24.0", lock.MinimumGoVersion)
	}
	root := t.TempDir()
	if err := lock.RestoreRootModule(root, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "\ngo 1.24.0\n") {
		t.Fatalf("restored go directive is not the module floor:\n%s", data)
	}
}

func TestLockRejectsMinimumGoVersionBelowModuleRequirement(t *testing.T) {
	t.Parallel()
	lock := fixtureGraphLock("/machine/provider-a", "/machine/provider-b", "/machine/consumer")
	lock.MinimumGoVersion = "go1.23.0"
	err := lock.Validate()
	if err == nil || !strings.Contains(err.Error(), "INGOT-LOCK-TOOLCHAIN") || !strings.Contains(err.Error(), "1.24.0") {
		t.Fatalf("low minimum error = %v", err)
	}
}

func TestLockRejectsMalformedModuleGoVersion(t *testing.T) {
	t.Parallel()
	lock := fixtureGraphLock("/machine/provider-a", "/machine/provider-b", "/machine/consumer")
	lock.Modules[0].GoVersion = "devel"
	err := lock.Validate()
	if err == nil || !strings.Contains(err.Error(), "go_version") {
		t.Fatalf("malformed module go version error = %v", err)
	}
}

// TestLegacyLockFallsBackToBuilderGoVersion keeps locks resolved before module
// language versions were recorded buildable with their previous meaning.
func TestLegacyLockFallsBackToBuilderGoVersion(t *testing.T) {
	t.Parallel()
	lock := fixtureGraphLock("/machine/provider-a", "/machine/provider-b", "/machine/consumer")
	lock.MinimumGoVersion = ""
	lock.Modules[0].GoVersion = ""
	lock.Modules[1].GoVersion = ""
	if err := lock.Validate(); err != nil {
		t.Fatal(err)
	}
	if lock.MinimumGoVersion != runtime.Version() {
		t.Fatalf("legacy minimum = %q, want %q", lock.MinimumGoVersion, runtime.Version())
	}
}

func TestProbeGoToolchainReportsCanonicalRelease(t *testing.T) {
	t.Parallel()
	version, err := probeGoToolchain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if canonical, ok := normalizeGoVersion(version); !ok || canonical != version {
		t.Fatalf("probed toolchain %q is not canonical", version)
	}
	if compareGoVersions(version, MinimumGeneratedGoVersion) < 0 {
		t.Fatalf("probed toolchain %q is older than the generated floor", version)
	}
}

func TestRequireGoToolchainNamesTheRaisingModule(t *testing.T) {
	t.Parallel()
	requirement := goRequirement{Version: "go1.25.0", Source: "example.com/plugin@v1.0.0"}
	err := requireGoToolchain("go1.24.2", requirement)
	if err == nil || !strings.Contains(err.Error(), "INGOT-BUILD-TOOLCHAIN") || !strings.Contains(err.Error(), "example.com/plugin@v1.0.0") {
		t.Fatalf("toolchain error = %v", err)
	}
	if err := requireGoToolchain("go1.25.0", requirement); err != nil {
		t.Fatalf("newer toolchain rejected: %v", err)
	}
	if err := requireGoToolchain("go1.26.2", requirement); err != nil {
		t.Fatalf("newest toolchain rejected: %v", err)
	}
}

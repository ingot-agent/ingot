package builder

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLockSerializationIsTargetNeutral(t *testing.T) {
	t.Parallel()
	lock := fixtureGraphLock("/dev/placeholder-a", "/dev/placeholder-b", "/dev/placeholder-c")
	data, err := lock.MarshalTOML()
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"[toolchain]", "[target]", "[environment]", "[build]", "goos =", "goarch ="} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("target-specific field %q entered lock:\n%s", forbidden, data)
		}
	}
	path := filepath.Join(t.TempDir(), "plugins.lock")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Target.GOOS != runtime.GOOS || parsed.Target.GOARCH != runtime.GOARCH || parsed.Toolchain.Version != runtime.Version() {
		t.Fatalf("ephemeral build facts = %#v %#v", parsed.Target, parsed.Toolchain)
	}
}

func TestLockRoundTripKeepsIdentity(t *testing.T) {
	t.Parallel()
	// A freshly resolved lock carries nil build-flag slices (they are
	// serialized as null); parsing it back from the TOML file yields empty
	// non-nil slices. ImageID must not depend on that distinction.
	lock := fixtureGraphLock("/dev/placeholder-a", "/dev/placeholder-b", "/dev/placeholder-c")
	lock.Build.Tags = nil
	lock.Build.LDFlags = nil
	lock.Build.GCFlags = nil
	lock.Build.ASMFlags = nil
	lock.Target.GOExperiment = nil
	before, err := lock.ImageID()
	if err != nil {
		t.Fatal(err)
	}
	data, err := lock.MarshalTOML()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "plugins.lock")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseLock(path)
	if err != nil {
		t.Fatal(err)
	}
	after, err := parsed.ImageID()
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("lock round trip changed ImageID:\n before: %s\n after:  %s", before, after)
	}
	// The canonical build manifest must equal the parsed lock's manifest too.
	canonical, err := parsed.CanonicalBuildManifest()
	if err != nil {
		t.Fatal(err)
	}
	if digestBytes(canonical) != after {
		t.Fatal("canonical manifest digest does not match ImageID")
	}
}

func TestLockRoundTripPreservesModuleGraph(t *testing.T) {
	t.Parallel()
	lock := fixtureGraphLock("/dev/placeholder-a", "/dev/placeholder-b", "/dev/placeholder-c")
	// Insert a second graph node at the correct sorted position.
	lock.Modules = append([]LockedModule{{Path: "example.com/indirect", Version: "v1.2.3", Sum: "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", GoModSum: "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}}, lock.Modules...)
	data, err := lock.MarshalTOML()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "plugins.lock")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Modules) != len(lock.Modules) {
		t.Fatalf("round trip changed module count: %d != %d", len(parsed.Modules), len(lock.Modules))
	}
}

func TestLockLocalReplacementVersions(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, suffix, version string
		valid                 bool
	}{
		{name: "initial", version: "v0.0.0", valid: true},
		{name: "selected", version: "v0.1.1", valid: true},
		{name: "pseudo", version: "v0.0.0-20260822091230-0123456789ab", valid: true},
		{name: "v2", suffix: "/v2", version: "v2.1.1", valid: true},
		{name: "empty"},
		{name: "noncanonical", version: "v0.1"},
		{name: "wrong-major", version: "v2.1.1"},
		{name: "v2-wrong-major", suffix: "/v2", version: "v1.1.1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			lock := fixtureGraphLock("/dev/a", "/dev/b", "/dev/c")
			modulePath := lock.Replacements[0].ModulePath
			for i := range lock.Plugins {
				if lock.Plugins[i].ID == modulePath {
					lock.Plugins[i].ID += test.suffix
				}
			}
			lock.Replacements[0].ModulePath += test.suffix
			lock.Replacements[0].SyntheticVersion = test.version
			err := lock.Validate()
			if test.valid && err != nil {
				t.Fatal(err)
			}
			if !test.valid && (err == nil || !strings.Contains(err.Error(), "INGOT-LOCK-SYNTHETIC-VERSION")) {
				t.Fatalf("invalid version error = %v", err)
			}
		})
	}
}

func TestImageIDIncludesReplacementVersion(t *testing.T) {
	t.Parallel()
	lock := fixtureGraphLock("/dev/a", "/dev/b", "/dev/c")
	initial, err := lock.ImageID()
	if err != nil {
		t.Fatal(err)
	}
	lock.Replacements[0].SyntheticVersion = "v0.1.1"
	selected, err := lock.ImageID()
	if err != nil {
		t.Fatal(err)
	}
	if selected == initial {
		t.Fatal("replacement version must affect ImageID because it enters Go build info")
	}
}

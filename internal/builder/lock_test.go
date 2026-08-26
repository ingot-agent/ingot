package builder

import (
	"os"
	"path/filepath"
	"testing"
)

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

package bundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testDistribution points at the repository's plugins/ directory, which is
// the official plugin set this package is tested against.
func testDistribution(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "plugins")
	if !validDistribution(directory) {
		t.Fatalf("test distribution %s is incomplete", directory)
	}
	return directory
}

func TestProfilesCoverOfficialPlugins(t *testing.T) {
	t.Parallel()
	distribution := testDistribution(t)
	for _, name := range []string{"default", "minimal"} {
		profile, err := LookupProfile(name)
		if err != nil {
			t.Fatal(err)
		}
		if len(profile.Plugins) == 0 {
			t.Fatalf("profile %s is empty", name)
		}
		seen := map[string]bool{}
		for _, directory := range profile.Plugins {
			if seen[directory] {
				t.Fatalf("profile %s repeats %s", name, directory)
			}
			seen[directory] = true
			if !validPluginDirectory(filepath.Join(distribution, directory)) {
				t.Fatalf("profile %s references missing plugin directory %s", name, directory)
			}
		}
	}
	if _, err := LookupProfile("does-not-exist"); err == nil {
		t.Fatal("unknown profile must fail")
	}
}

func TestLocateExplicitBundle(t *testing.T) {
	t.Parallel()
	distribution := testDistribution(t)
	locator, err := Locate(distribution)
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := filepath.Abs(distribution)
	if locator != expected {
		t.Fatalf("located %q, want %q", locator, expected)
	}
	if _, err := Locate(t.TempDir()); err == nil {
		t.Fatal("a directory without the official plugins must be rejected")
	}
}

func TestMaterializeIsIdempotent(t *testing.T) {
	t.Parallel()
	distribution := testDistribution(t)
	profile, err := LookupProfile("default")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	first, err := Materialize(distribution, home, profile)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(profile.Plugins) {
		t.Fatalf("materialized %d entries, want %d", len(first), len(profile.Plugins))
	}
	for _, entry := range first {
		if entry.Directory == "" || !strings.Contains(entry.Module, "github.com/ingot-agent/") || entry.Name == "" {
			t.Fatalf("invalid entry %#v", entry)
		}
		if info, err := os.Stat(filepath.Join(home, BundledDirectory, entry.Directory, "go.mod")); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("materialized go.mod missing for %s: %v", entry.Directory, err)
		}
	}
	marker := filepath.Join(home, BundledDirectory, markerName)
	firstInfo, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Materialize(distribution, home, profile)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != len(first) {
		t.Fatalf("second materialization changed entry count")
	}
	secondInfo, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}
	if !firstInfo.ModTime().Equal(secondInfo.ModTime()) {
		t.Fatal("unchanged distribution must not rewrite the bundle")
	}
}

func TestMaterializeRefreshesOnDistributionChange(t *testing.T) {
	t.Parallel()
	distribution := testDistribution(t)
	profile, err := LookupProfile("default")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if _, err := Materialize(distribution, home, profile); err != nil {
		t.Fatal(err)
	}
	pluginRoot := filepath.Join(home, BundledDirectory, profile.Plugins[0])
	stray := filepath.Join(pluginRoot, "stray.txt")
	if err := os.WriteFile(stray, []byte("user data"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A changed source distribution invalidates the marker and fully rewrites
	// the bundle, removing stale content.
	sourceStat, err := os.Stat(filepath.Join(distribution, profile.Plugins[0], "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	corruptedMarker := filepath.Join(home, BundledDirectory, markerName)
	if err := os.WriteFile(corruptedMarker, []byte("sha256:deadbeef\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Materialize(distribution, home, profile); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatal("stale materialized file was not removed on refresh")
	}
	refreshedStat, err := os.Stat(filepath.Join(distribution, profile.Plugins[0], "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if sourceStat.Size() != refreshedStat.Size() {
		t.Fatal("distribution changed unexpectedly")
	}
}

func TestMaterializeRejectsBrokenDistribution(t *testing.T) {
	t.Parallel()
	profile, err := LookupProfile("default")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if _, err := Materialize(home, home, profile); err == nil {
		t.Fatal("a home directory without plugin sources must be rejected")
	}
}

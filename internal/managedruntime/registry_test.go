package managedruntime

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ingot-agent/ingot/internal/image"
)

func binding(digest string) image.Binding {
	return image.Binding{Target: image.Target{GOOS: "linux", GOARCH: "amd64", GOExperiment: []string{}, Tuning: []image.TargetKey{}}, ImageID: "sha256:" + strings.Repeat(digest, 64), ArtifactDigest: "sha256:" + strings.Repeat("a", 64)}
}

func TestCreateSwitchRollbackAndCommandGeneration(t *testing.T) {
	registry := New(filepath.Join(t.TempDir(), "runtimes"))
	now := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	entry, err := registry.Create("work", binding("1"), []string{"web"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Generation != 1 {
		t.Fatal(entry.Generation)
	}
	entry, changed, err := registry.Switch("work", binding("1"), now.Add(time.Second))
	if err != nil || changed || entry.Generation != 1 {
		t.Fatalf("same switch=%#v %v", entry, err)
	}
	entry, changed, err = registry.Switch("work", binding("2"), now.Add(time.Second))
	if err != nil || !changed || entry.Generation != 2 || entry.RollbackImage == nil {
		t.Fatalf("switch=%#v %v", entry, err)
	}
	entry, err = registry.Rollback("work", now.Add(2*time.Second))
	if err != nil || entry.DesiredImage.ImageID != binding("1").ImageID || entry.Generation != 3 {
		t.Fatalf("rollback=%#v %v", entry, err)
	}
	entry, changed, err = registry.SetCommand("work", []string{"serve"}, now.Add(3*time.Second))
	if err != nil || !changed || entry.Generation != 4 {
		t.Fatalf("command=%#v %v", entry, err)
	}
}

func TestRuntimeNameRejectsTraversal(t *testing.T) {
	registry := New(filepath.Join(t.TempDir(), "runtimes"))
	if _, err := registry.Create("../bad", binding("1"), nil, time.Now()); err == nil {
		t.Fatal("traversal runtime name accepted")
	}
}

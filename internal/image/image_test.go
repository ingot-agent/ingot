package image

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ingot-agent/ingot/internal/layout"
)

func TestReferenceGrammar(t *testing.T) {
	valid := []string{"sha256:" + strings.Repeat("a", 64), "acme/app:1.0.0", "acme/app:stable@linux/amd64"}
	for _, value := range valid {
		if _, err := ParseReference(value); err != nil {
			t.Errorf("%s: %v", value, err)
		}
	}
	invalid := []string{"acme/app", "Acme/app:tag", "acme/app:TAG", "acme/app:tag@linux", "sha256:../../x"}
	for _, value := range invalid {
		if _, err := ParseReference(value); err == nil {
			t.Errorf("accepted %s", value)
		}
	}
}

func TestCatalogVariantMoveAndSorting(t *testing.T) {
	catalog := NewCatalog()
	target := Target{GOOS: "linux", GOARCH: "amd64", GOExperiment: []string{}, Tuning: []TargetKey{}}
	first := Binding{Target: target, ImageID: "sha256:" + strings.Repeat("1", 64), ArtifactDigest: "sha256:" + strings.Repeat("2", 64)}
	second := first
	second.ImageID = "sha256:" + strings.Repeat("3", 64)
	source := Source{Name: "acme/app", Tag: "stable"}
	if err := catalog.Set(source, first); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Set(source, second); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Tags) != 1 || len(catalog.Tags[0].Variants) != 1 || catalog.Tags[0].Variants[0].ImageID != second.ImageID {
		t.Fatalf("catalog=%#v", catalog)
	}
	if err := catalog.Pin(first.ImageID); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestBundleRejectsExtraEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.ingot-image")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	for _, name := range []string{"bundle.json", "manifest.json", "ingot-runtime", "extra"} {
		writer, _ := archive.Create(name)
		_, _ = writer.Write([]byte("x"))
	}
	_ = archive.Close()
	_ = file.Close()
	if _, err := ImportBundle(t.TempDir(), path); err == nil {
		t.Fatal("extra bundle entry accepted")
	}
}

func TestVerifyManifestV3(t *testing.T) {
	directory := t.TempDir()
	target := Target{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, GOExperiment: []string{}, Tuning: []TargetKey{}}
	build, err := CanonicalJSON(map[string]any{"target": map[string]any{"goos": runtime.GOOS, "goarch": runtime.GOARCH, "cgo_enabled": false, "goexperiment": []string{}, "tuning": map[string]string{}}, "value": 1})
	if err != nil {
		t.Fatal(err)
	}
	id := Digest(build)
	binary := filepath.Join(directory, layout.RuntimeExecutableName(runtime.GOOS))
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	artifact, _ := FileDigest(binary)
	manifest := Manifest{SchemaVersion: 3, ImageID: id, ArtifactDigest: artifact, Target: target, BuildManifest: build, DirectPlugins: []string{}, ComponentCreationOrder: []string{}, ManyOrder: map[string][]string{}, HostDependencies: map[string][]string{}}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(directory, id, nil); err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte(artifact), []byte("sha256:"+strings.Repeat("f", 64)), 1)
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(directory, id, nil); err == nil {
		t.Fatal("corrupt artifact digest accepted")
	}
}

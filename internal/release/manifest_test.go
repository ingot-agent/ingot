package release

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestManifestRoundTripAndTargetLookup(t *testing.T) {
	data, err := json.Marshal(validTestManifest())
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := ParseManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := manifest.ArtifactFor("linux", "amd64")
	if err != nil || artifact.Name == "" {
		t.Fatalf("ArtifactFor() = %#v, %v", artifact, err)
	}
}

func TestManifestRejectsUnknownFieldsAndUnsafeAssets(t *testing.T) {
	manifest := validTestManifest()
	manifest.Artifacts[0].Name = "../ingot"
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseManifest(data); err == nil {
		t.Fatal("ParseManifest accepted an unsafe asset name")
	}

	valid, err := json.Marshal(validTestManifest())
	if err != nil {
		t.Fatal(err)
	}
	withUnknown := fmt.Sprintf(`%s,"unknown":true}`, strings.TrimSuffix(string(valid), "}"))
	if _, err := ParseManifest([]byte(withUnknown)); err == nil {
		t.Fatal("ParseManifest accepted an unknown field")
	}
}

func TestManifestRequiresCompleteCanonicalTargetSet(t *testing.T) {
	for _, mutate := range []func(*Manifest){
		func(manifest *Manifest) { manifest.Artifacts = manifest.Artifacts[:len(manifest.Artifacts)-1] },
		func(manifest *Manifest) { manifest.Artifacts[0].GOARCH = "386" },
		func(manifest *Manifest) { manifest.Artifacts[0].Name = "renamed.tar.gz" },
	} {
		manifest := validTestManifest()
		mutate(&manifest)
		if err := manifest.Validate(); err == nil {
			t.Fatalf("Validate accepted %#v", manifest.Artifacts)
		}
	}
}

func validTestManifest() Manifest {
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Tag:           "v0.3.1",
		Version:       "0.3.1",
		Commit:        "0123456789abcdef0123456789abcdef01234567",
	}
	for _, target := range CoreTargets {
		executable := "ingot"
		if target.GOOS == "windows" {
			executable = "ingot.exe"
		}
		manifest.Artifacts = append(manifest.Artifacts, Artifact{
			GOOS: target.GOOS, GOARCH: target.GOARCH,
			Name: coreArchiveName(manifest.Tag, target), SHA256: strings.Repeat("a", 64),
			Size: 100, Executable: executable,
		})
	}
	return manifest
}

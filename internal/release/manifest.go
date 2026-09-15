// Package release defines and produces the immutable core release assets.
package release

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/mod/semver"
)

const (
	ManifestSchemaVersion = 1
	MaxManifestSize       = 1 << 20
	MaxArchiveSize        = 256 << 20
)

var (
	digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	commitPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	assetPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

type Manifest struct {
	SchemaVersion int        `json:"schema_version"`
	Tag           string     `json:"tag"`
	Version       string     `json:"version"`
	Commit        string     `json:"commit"`
	Artifacts     []Artifact `json:"artifacts"`
}

type Artifact struct {
	GOOS       string `json:"goos"`
	GOARCH     string `json:"goarch"`
	Name       string `json:"name"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	Executable string `json:"executable"`
}

func ParseManifest(data []byte) (Manifest, error) {
	if len(data) > MaxManifestSize {
		return Manifest{}, fmt.Errorf("release manifest exceeds %d bytes", MaxManifestSize)
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode release manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Manifest{}, fmt.Errorf("decode release manifest: trailing data")
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (manifest Manifest) Validate() error {
	if manifest.SchemaVersion != ManifestSchemaVersion {
		return fmt.Errorf("unsupported release manifest schema %d", manifest.SchemaVersion)
	}
	if manifest.Version == "" || strings.HasPrefix(manifest.Version, "v") || strings.Contains(manifest.Version, "+") {
		return fmt.Errorf("invalid release version %q", manifest.Version)
	}
	if manifest.Tag != "v"+manifest.Version || !semver.IsValid(manifest.Tag) || semver.Canonical(manifest.Tag) != manifest.Tag {
		return fmt.Errorf("invalid release tag %q for version %q", manifest.Tag, manifest.Version)
	}
	if !commitPattern.MatchString(manifest.Commit) {
		return fmt.Errorf("invalid release commit %q", manifest.Commit)
	}
	if len(manifest.Artifacts) != len(CoreTargets) {
		return fmt.Errorf("release manifest has %d artifacts, want %d", len(manifest.Artifacts), len(CoreTargets))
	}
	expected := make(map[string]Target, len(CoreTargets))
	for _, target := range CoreTargets {
		expected[target.GOOS+"/"+target.GOARCH] = target
	}
	seenTargets := make(map[string]bool, len(manifest.Artifacts))
	seenNames := make(map[string]bool, len(manifest.Artifacts))
	for index, artifact := range manifest.Artifacts {
		target := artifact.GOOS + "/" + artifact.GOARCH
		expectedTarget, supported := expected[target]
		if !supported || seenTargets[target] {
			return fmt.Errorf("invalid or duplicate release target %q at artifacts[%d]", target, index)
		}
		seenTargets[target] = true
		if !assetPattern.MatchString(artifact.Name) || filepath.Base(artifact.Name) != artifact.Name {
			return fmt.Errorf("invalid asset name %q", artifact.Name)
		}
		wantName := coreArchiveName(manifest.Tag, expectedTarget)
		if artifact.Name != wantName || seenNames[artifact.Name] {
			return fmt.Errorf("invalid or duplicate asset name %q for target %s, want %q", artifact.Name, target, wantName)
		}
		seenNames[artifact.Name] = true
		if !digestPattern.MatchString(artifact.SHA256) {
			return fmt.Errorf("invalid SHA-256 for asset %q", artifact.Name)
		}
		if artifact.Size <= 0 || artifact.Size > MaxArchiveSize {
			return fmt.Errorf("invalid size %d for asset %q", artifact.Size, artifact.Name)
		}
		wantExecutable := "ingot"
		if artifact.GOOS == "windows" {
			wantExecutable = "ingot.exe"
		}
		if artifact.Executable != wantExecutable {
			return fmt.Errorf("asset %q executable is %q, want %q", artifact.Name, artifact.Executable, wantExecutable)
		}
	}
	return nil
}

func coreArchiveName(tag string, target Target) string {
	extension := ".tar.gz"
	if target.GOOS == "windows" {
		extension = ".zip"
	}
	return "ingot-" + tag + "-" + target.GOOS + "-" + target.GOARCH + extension
}

func (manifest Manifest) ArtifactFor(goos, goarch string) (Artifact, error) {
	for _, artifact := range manifest.Artifacts {
		if artifact.GOOS == goos && artifact.GOARCH == goarch {
			return artifact, nil
		}
	}
	return Artifact{}, fmt.Errorf("release %s has no asset for %s/%s", manifest.Tag, goos, goarch)
}

package image

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"

	"github.com/ingot-agent/ingot/internal/layout"
)

type Manifest struct {
	SchemaVersion          int                 `json:"schema_version"`
	ImageID                string              `json:"image_id"`
	ArtifactDigest         string              `json:"artifact_digest"`
	Target                 Target              `json:"target"`
	BuildManifest          json.RawMessage     `json:"build_manifest"`
	DirectPlugins          []string            `json:"direct_plugins"`
	ComponentCreationOrder []string            `json:"component_creation_order"`
	ManyOrder              map[string][]string `json:"many_order"`
	HostDependencies       map[string][]string `json:"host_dependencies"`
}

type Verified struct {
	Manifest   Manifest
	Directory  string
	BinaryPath string
}

func StrictDecode(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

func ReadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := StrictDecode(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("INGOT-IMAGE-MANIFEST-SCHEMA: %s: %w", path, err)
	}
	if manifest.SchemaVersion != 3 {
		return Manifest{}, fmt.Errorf("INGOT-IMAGE-MANIFEST-VERSION: want 3, got %d", manifest.SchemaVersion)
	}
	if err := manifest.Target.Validate(); err != nil {
		return Manifest{}, err
	}
	if !ValidDigest(manifest.ImageID) || !ValidDigest(manifest.ArtifactDigest) {
		return Manifest{}, fmt.Errorf("INGOT-IMAGE-MANIFEST-DIGEST: invalid digest")
	}
	if manifest.DirectPlugins == nil || manifest.ComponentCreationOrder == nil || manifest.ManyOrder == nil || manifest.HostDependencies == nil {
		return Manifest{}, fmt.Errorf("INGOT-IMAGE-MANIFEST-SCHEMA: collection fields must be present")
	}
	return manifest, nil
}

func TargetFromBuildManifest(raw json.RawMessage) (Target, error) {
	var projection struct {
		Target struct {
			GOOS         string            `json:"goos"`
			GOARCH       string            `json:"goarch"`
			CGOEnabled   bool              `json:"cgo_enabled"`
			GOExperiment []string          `json:"goexperiment"`
			Tuning       map[string]string `json:"tuning"`
		} `json:"target"`
	}
	if err := json.Unmarshal(raw, &projection); err != nil {
		return Target{}, err
	}
	target := Target{GOOS: projection.Target.GOOS, GOARCH: projection.Target.GOARCH, CGOEnabled: projection.Target.CGOEnabled, GOExperiment: append([]string{}, projection.Target.GOExperiment...)}
	keys := make([]string, 0, len(projection.Target.Tuning))
	for key := range projection.Target.Tuning {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		target.Tuning = append(target.Tuning, TargetKey{Key: key, Value: projection.Target.Tuning[key]})
	}
	if target.GOExperiment == nil {
		target.GOExperiment = []string{}
	}
	if target.Tuning == nil {
		target.Tuning = []TargetKey{}
	}
	return target, target.Validate()
}

func Verify(directory, expectedImageID string, expectedBuildManifest []byte) (Verified, error) {
	manifest, err := ReadManifest(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return Verified{}, err
	}
	canonical, err := CanonicalJSON(manifest.BuildManifest)
	if err != nil {
		return Verified{}, err
	}
	computed := Digest(canonical)
	if expectedImageID == "" {
		expectedImageID = manifest.ImageID
	}
	if manifest.ImageID != expectedImageID || computed != expectedImageID {
		return Verified{}, fmt.Errorf("INGOT-IMAGE-MANIFEST-IDENTITY: want %s, manifest %s, computed %s", expectedImageID, manifest.ImageID, computed)
	}
	projected, err := TargetFromBuildManifest(manifest.BuildManifest)
	if err != nil {
		return Verified{}, err
	}
	if !reflect.DeepEqual(projected, manifest.Target) {
		return Verified{}, fmt.Errorf("INGOT-IMAGE-TARGET-PROJECTION: top-level target differs from build manifest")
	}
	if len(expectedBuildManifest) > 0 {
		expected, err := CanonicalJSON(json.RawMessage(expectedBuildManifest))
		if err != nil {
			return Verified{}, err
		}
		if !bytes.Equal(expected, canonical) {
			return Verified{}, fmt.Errorf("INGOT-IMAGE-BUILD-MANIFEST: expected build manifest differs")
		}
	}
	binary := filepath.Join(directory, layout.RuntimeExecutableName(manifest.Target.GOOS))
	digest, err := FileDigest(binary)
	if err != nil {
		return Verified{}, err
	}
	if digest != manifest.ArtifactDigest {
		return Verified{}, fmt.Errorf("INGOT-IMAGE-ARTIFACT: want %s, got %s", manifest.ArtifactDigest, digest)
	}
	return Verified{Manifest: manifest, Directory: directory, BinaryPath: binary}, nil
}

func FileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func CanonicalJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var generic any
	if err := decoder.Decode(&generic); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := writeCanonical(&out, generic); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeCanonical(out *bytes.Buffer, value any) error {
	switch value := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		out.WriteString(strconv.FormatBool(value))
	case string:
		data, _ := json.Marshal(value)
		out.Write(data)
	case json.Number:
		if _, err := strconv.ParseInt(string(value), 10, 64); err != nil {
			return fmt.Errorf("non-integral canonical number %q", value)
		}
		out.WriteString(string(value))
	case []any:
		out.WriteByte('[')
		for i, item := range value {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeCanonical(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			encoded, _ := json.Marshal(key)
			out.Write(encoded)
			out.WriteByte(':')
			if err := writeCanonical(out, value[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("unsupported canonical JSON value %s", reflect.TypeOf(value))
	}
	return nil
}

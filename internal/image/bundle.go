package image

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/ingot-agent/ingot/internal/layout"
)

const (
	MaxBundleSize     = int64(1 << 30)
	MaxExecutableSize = int64(1 << 30)
	MaxJSONSize       = int64(4 << 20)
)

var zipEpoch = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

type BundleDescriptor struct {
	BundleVersion  int     `json:"bundle_version"`
	Tag            *Source `json:"tag"`
	Target         Target  `json:"target"`
	ImageID        string  `json:"image_id"`
	ArtifactDigest string  `json:"artifact_digest"`
}

type ImportResult struct {
	Binding Binding `json:"binding"`
	Tag     *Source `json:"tag"`
	Created bool    `json:"created"`
}

func ExportBundle(directory string, source *Source, output string) error {
	verified, err := Verify(directory, "", nil)
	if err != nil {
		return err
	}
	descriptor := BundleDescriptor{BundleVersion: 1, Tag: source, Target: verified.Manifest.Target, ImageID: verified.Manifest.ImageID, ArtifactDigest: verified.Manifest.ArtifactDigest}
	descriptorData, err := json.MarshalIndent(descriptor, "", "  ")
	if err != nil {
		return err
	}
	descriptorData = append(descriptorData, '\n')
	manifestData, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return err
	}
	var normalizedManifest Manifest
	if err := StrictDecode(manifestData, &normalizedManifest); err != nil {
		return err
	}
	manifestData, err = json.MarshalIndent(normalizedManifest, "", "  ")
	if err != nil {
		return err
	}
	manifestData = append(manifestData, '\n')
	destinationDir := filepath.Dir(output)
	temporary, err := os.CreateTemp(destinationDir, ".ingot-image-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(temporaryPath)
		}
	}()
	archive := zip.NewWriter(temporary)
	entries := []struct {
		name string
		mode os.FileMode
		data []byte
		path string
	}{
		{name: "bundle.json", mode: 0o644, data: descriptorData},
		{name: "manifest.json", mode: 0o644, data: manifestData},
		{name: layout.RuntimeExecutableName(verified.Manifest.Target.GOOS), mode: 0o755, path: verified.BinaryPath},
	}
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		header.SetModTime(zipEpoch)
		header.SetMode(entry.mode)
		writer, err := archive.CreateHeader(header)
		if err != nil {
			archive.Close()
			temporary.Close()
			return err
		}
		if entry.path != "" {
			file, err := os.Open(entry.path)
			if err != nil {
				archive.Close()
				temporary.Close()
				return err
			}
			_, copyErr := io.Copy(writer, file)
			closeErr := file.Close()
			if copyErr != nil {
				archive.Close()
				temporary.Close()
				return copyErr
			}
			if closeErr != nil {
				archive.Close()
				temporary.Close()
				return closeErr
			}
		} else if _, err := writer.Write(entry.data); err != nil {
			archive.Close()
			temporary.Close()
			return err
		}
	}
	if err := archive.Close(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := replaceFile(temporaryPath, output); err != nil {
		return err
	}
	remove = false
	return syncDirectory(destinationDir)
}

func ImportBundle(imagesDirectory, bundlePath string) (ImportResult, error) {
	return ImportBundleWithPrepare(imagesDirectory, bundlePath, nil)
}

// ImportBundleWithPrepare invokes prepare after the bundle has been fully
// verified but before a new image directory is committed. The hook lets the
// Home layer persist its crash-recovery journal before the store mutation.
func ImportBundleWithPrepare(imagesDirectory, bundlePath string, prepare func(ImportResult) error) (ImportResult, error) {
	info, err := os.Stat(bundlePath)
	if err != nil {
		return ImportResult{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxBundleSize {
		return ImportResult{}, fmt.Errorf("INGOT-IMAGE-BUNDLE-SIZE: bundle must be a regular file no larger than 1 GiB")
	}
	reader, err := zip.OpenReader(bundlePath)
	if err != nil {
		return ImportResult{}, fmt.Errorf("INGOT-IMAGE-BUNDLE-ZIP: %w", err)
	}
	defer reader.Close()
	if len(reader.File) != 3 {
		return ImportResult{}, fmt.Errorf("INGOT-IMAGE-BUNDLE-ENTRIES: want exactly 3 entries")
	}
	files := map[string]*zip.File{}
	var totalUncompressed uint64
	totalLimit := uint64(MaxExecutableSize + 2*MaxJSONSize)
	for _, file := range reader.File {
		name := file.Name
		if name == "" || filepath.IsAbs(name) || strings.Contains(name, "\\") || strings.Contains(name, "/") || name == "." || name == ".." || !file.Mode().IsRegular() {
			return ImportResult{}, fmt.Errorf("INGOT-IMAGE-BUNDLE-PATH: invalid entry %q", name)
		}
		if _, duplicate := files[name]; duplicate {
			return ImportResult{}, fmt.Errorf("INGOT-IMAGE-BUNDLE-DUPLICATE: %q", name)
		}
		if file.UncompressedSize64 > totalLimit-totalUncompressed {
			return ImportResult{}, fmt.Errorf("INGOT-IMAGE-BUNDLE-SIZE: total uncompressed size exceeds limit")
		}
		totalUncompressed += file.UncompressedSize64
		files[name] = file
	}
	bundleFile, ok := files["bundle.json"]
	if !ok {
		return ImportResult{}, fmt.Errorf("INGOT-IMAGE-BUNDLE-ENTRIES: missing bundle.json")
	}
	manifestFile, ok := files["manifest.json"]
	if !ok {
		return ImportResult{}, fmt.Errorf("INGOT-IMAGE-BUNDLE-ENTRIES: missing manifest.json")
	}
	bundleData, err := readZipBounded(bundleFile, MaxJSONSize)
	if err != nil {
		return ImportResult{}, err
	}
	manifestData, err := readZipBounded(manifestFile, MaxJSONSize)
	if err != nil {
		return ImportResult{}, err
	}
	var descriptor BundleDescriptor
	if err := StrictDecode(bundleData, &descriptor); err != nil {
		return ImportResult{}, fmt.Errorf("INGOT-IMAGE-BUNDLE-DESCRIPTOR: %w", err)
	}
	if descriptor.BundleVersion != 1 || !ValidDigest(descriptor.ImageID) || !ValidDigest(descriptor.ArtifactDigest) {
		return ImportResult{}, fmt.Errorf("INGOT-IMAGE-BUNDLE-DESCRIPTOR: invalid version or digest")
	}
	if err := descriptor.Target.Validate(); err != nil {
		return ImportResult{}, err
	}
	if descriptor.Tag != nil {
		if err := ValidateName(descriptor.Tag.Name); err != nil {
			return ImportResult{}, err
		}
		if err := ValidateTag(descriptor.Tag.Tag); err != nil {
			return ImportResult{}, err
		}
	}
	var manifest Manifest
	if err := StrictDecode(manifestData, &manifest); err != nil {
		return ImportResult{}, fmt.Errorf("INGOT-IMAGE-BUNDLE-MANIFEST: %w", err)
	}
	if manifest.SchemaVersion != 3 || manifest.ImageID != descriptor.ImageID || manifest.ArtifactDigest != descriptor.ArtifactDigest || !targetsEqual(manifest.Target, descriptor.Target) {
		return ImportResult{}, fmt.Errorf("INGOT-IMAGE-BUNDLE-MISMATCH: descriptor and manifest differ")
	}
	executableName := layout.RuntimeExecutableName(descriptor.Target.GOOS)
	executable, ok := files[executableName]
	if !ok {
		return ImportResult{}, fmt.Errorf("INGOT-IMAGE-BUNDLE-ENTRIES: missing %s", executableName)
	}
	for name := range files {
		if name != "bundle.json" && name != "manifest.json" && name != executableName {
			return ImportResult{}, fmt.Errorf("INGOT-IMAGE-BUNDLE-ENTRIES: unknown entry %s", name)
		}
	}
	if executable.UncompressedSize64 > uint64(MaxExecutableSize) {
		return ImportResult{}, fmt.Errorf("INGOT-IMAGE-BUNDLE-SIZE: executable exceeds 1 GiB")
	}
	if err := os.MkdirAll(imagesDirectory, 0o700); err != nil {
		return ImportResult{}, err
	}
	staging, err := os.MkdirTemp(imagesDirectory, ".staging-")
	if err != nil {
		return ImportResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(staging)
		} else {
			_ = os.RemoveAll(staging)
		}
	}()
	if err := AtomicWrite(filepath.Join(staging, "manifest.json"), append(bytes.TrimSpace(manifestData), '\n'), 0o644); err != nil {
		return ImportResult{}, err
	}
	input, err := executable.Open()
	if err != nil {
		return ImportResult{}, err
	}
	output, err := os.OpenFile(filepath.Join(staging, executableName), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		input.Close()
		return ImportResult{}, err
	}
	written, copyErr := io.Copy(output, io.LimitReader(input, MaxExecutableSize+1))
	inputErr := input.Close()
	syncErr := output.Sync()
	outputErr := output.Close()
	if copyErr != nil {
		return ImportResult{}, copyErr
	}
	if inputErr != nil {
		return ImportResult{}, inputErr
	}
	if syncErr != nil {
		return ImportResult{}, syncErr
	}
	if outputErr != nil {
		return ImportResult{}, outputErr
	}
	if err := syncDirectory(staging); err != nil {
		return ImportResult{}, err
	}
	if written > MaxExecutableSize {
		return ImportResult{}, fmt.Errorf("INGOT-IMAGE-BUNDLE-SIZE: executable exceeds limit")
	}
	verified, err := Verify(staging, descriptor.ImageID, nil)
	if err != nil {
		return ImportResult{}, err
	}
	if verified.Manifest.ArtifactDigest != descriptor.ArtifactDigest {
		return ImportResult{}, fmt.Errorf("INGOT-IMAGE-BUNDLE-MISMATCH: artifact digest differs")
	}
	binding := Binding{Source: descriptor.Tag, Target: descriptor.Target, ImageID: descriptor.ImageID, ArtifactDigest: descriptor.ArtifactDigest}
	result := ImportResult{Binding: binding, Tag: descriptor.Tag, Created: true}
	final := filepath.Join(imagesDirectory, layout.ImageDirectoryName(descriptor.ImageID, runtime.GOOS))
	if _, err := os.Stat(final); err == nil {
		existing, verifyErr := Verify(final, descriptor.ImageID, nil)
		if verifyErr != nil {
			return ImportResult{}, verifyErr
		}
		if existing.Manifest.ArtifactDigest != descriptor.ArtifactDigest || !targetsEqual(existing.Manifest.Target, descriptor.Target) {
			return ImportResult{}, fmt.Errorf("INGOT-IMAGE-BUNDLE-MISMATCH: existing image differs")
		}
		result.Created = false
	} else if !os.IsNotExist(err) {
		return ImportResult{}, err
	}
	if prepare != nil {
		if err := prepare(result); err != nil {
			return ImportResult{}, err
		}
	}
	if result.Created {
		if err := os.Rename(staging, final); err != nil {
			if _, verifyErr := Verify(final, descriptor.ImageID, nil); verifyErr != nil {
				return ImportResult{}, err
			}
			result.Created = false
		} else if err := syncDirectory(imagesDirectory); err != nil {
			return ImportResult{}, err
		}
	}
	committed = true
	return result, nil
}

func readZipBounded(file *zip.File, limit int64) ([]byte, error) {
	if file.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("INGOT-IMAGE-BUNDLE-SIZE: %s exceeds limit", file.Name)
	}
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("INGOT-IMAGE-BUNDLE-SIZE: %s exceeds limit", file.Name)
	}
	return data, nil
}

func targetsEqual(left, right Target) bool {
	return left.Equal(right)
}

func SortedPlatforms(entries []Variant) []string {
	result := make([]string, len(entries))
	for i, entry := range entries {
		result[i] = entry.Target.Platform()
	}
	sort.Strings(result)
	return result
}

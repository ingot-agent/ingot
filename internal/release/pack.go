package release

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

type Target struct {
	GOOS   string
	GOARCH string
}

var CoreTargets = []Target{
	{GOOS: "linux", GOARCH: "amd64"},
	{GOOS: "linux", GOARCH: "arm64"},
	{GOOS: "darwin", GOARCH: "amd64"},
	{GOOS: "darwin", GOARCH: "arm64"},
	{GOOS: "windows", GOARCH: "amd64"},
	{GOOS: "windows", GOARCH: "arm64"},
}

type PackOptions struct {
	Version         string
	Commit          string
	SourceTime      time.Time
	InputDirectory  string
	OutputDirectory string
	LicensePath     string
	InstallSHPath   string
	InstallPS1Path  string
}

// PackCoreRelease creates deterministic archives and their release metadata.
func PackCoreRelease(options PackOptions) (Manifest, error) {
	tag := options.Version
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	if !semver.IsValid(tag) || semver.Canonical(tag) != tag || strings.Contains(tag, "+") {
		return Manifest{}, fmt.Errorf("invalid release version %q", options.Version)
	}
	version := strings.TrimPrefix(tag, "v")
	if !commitPattern.MatchString(options.Commit) {
		return Manifest{}, fmt.Errorf("invalid release commit %q", options.Commit)
	}
	if options.SourceTime.IsZero() {
		return Manifest{}, fmt.Errorf("release source time is required")
	}
	if err := prepareOutputDirectory(options.OutputDirectory); err != nil {
		return Manifest{}, err
	}
	license, err := os.Open(options.LicensePath)
	if err != nil {
		return Manifest{}, err
	}
	licenseInfo, err := license.Stat()
	_ = license.Close()
	if err != nil {
		return Manifest{}, err
	}

	manifest := Manifest{SchemaVersion: ManifestSchemaVersion, Tag: tag, Version: version, Commit: options.Commit}
	for _, target := range CoreTargets {
		executable := "ingot"
		if target.GOOS == "windows" {
			executable = "ingot.exe"
		}
		inputName := "ingot-" + target.GOOS + "-" + target.GOARCH
		if target.GOOS == "windows" {
			inputName += ".exe"
		}
		inputPath := filepath.Join(options.InputDirectory, inputName)
		info, err := os.Stat(inputPath)
		if err != nil {
			return Manifest{}, fmt.Errorf("release input %s: %w", inputName, err)
		}
		if !info.Mode().IsRegular() || info.Size() <= 0 {
			return Manifest{}, fmt.Errorf("release input %s is not a non-empty regular file", inputName)
		}
		archiveName := coreArchiveName(tag, target)
		archivePath := filepath.Join(options.OutputDirectory, archiveName)
		if target.GOOS == "windows" {
			err = writeZipArchive(archivePath, inputPath, executable, options.LicensePath, options.SourceTime)
		} else {
			err = writeTarArchive(archivePath, inputPath, executable, info.Size(), options.LicensePath, licenseInfo.Size(), options.SourceTime)
		}
		if err != nil {
			return Manifest{}, err
		}
		digest, size, err := digestFile(archivePath)
		if err != nil {
			return Manifest{}, err
		}
		manifest.Artifacts = append(manifest.Artifacts, Artifact{
			GOOS: target.GOOS, GOARCH: target.GOARCH, Name: archiveName,
			SHA256: digest, Size: size, Executable: executable,
		})
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	if err := copyReleaseFile(options.InstallSHPath, filepath.Join(options.OutputDirectory, "install.sh"), 0o755); err != nil {
		return Manifest{}, err
	}
	if err := copyReleaseFile(options.InstallPS1Path, filepath.Join(options.OutputDirectory, "install.ps1"), 0o644); err != nil {
		return Manifest{}, err
	}
	if err := os.WriteFile(filepath.Join(options.OutputDirectory, "VERSION"), []byte(tag+"\n"), 0o644); err != nil {
		return Manifest{}, err
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Manifest{}, err
	}
	manifestData = append(manifestData, '\n')
	if err := os.WriteFile(filepath.Join(options.OutputDirectory, "release-manifest.json"), manifestData, 0o644); err != nil {
		return Manifest{}, err
	}
	if err := writeChecksums(options.OutputDirectory); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func prepareOutputDirectory(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("release output directory %s is not empty", path)
	}
	return nil
}

func writeTarArchive(outputPath, binaryPath, binaryName string, binarySize int64, licensePath string, licenseSize int64, sourceTime time.Time) error {
	output, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	gzipWriter, err := gzip.NewWriterLevel(output, gzip.BestCompression)
	if err != nil {
		_ = output.Close()
		return err
	}
	gzipWriter.Header.ModTime = sourceTime.UTC()
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	writeErr := writeTarFile(tarWriter, binaryPath, binaryName, binarySize, 0o755, sourceTime)
	if writeErr == nil {
		writeErr = writeTarFile(tarWriter, licensePath, "LICENSE", licenseSize, 0o644, sourceTime)
	}
	closeTarErr := tarWriter.Close()
	closeGzipErr := gzipWriter.Close()
	closeOutputErr := output.Close()
	for _, candidate := range []error{writeErr, closeTarErr, closeGzipErr, closeOutputErr} {
		if candidate != nil {
			return candidate
		}
	}
	return nil
}

func writeTarFile(writer *tar.Writer, sourcePath, name string, size int64, mode int64, sourceTime time.Time) error {
	header := &tar.Header{Name: name, Mode: mode, Size: size, ModTime: sourceTime.UTC(), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	return copyFileContent(writer, sourcePath)
}

func writeZipArchive(outputPath, binaryPath, binaryName, licensePath string, sourceTime time.Time) error {
	output, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	writer := zip.NewWriter(output)
	writeErr := writeZipFile(writer, binaryPath, binaryName, 0o755, sourceTime)
	if writeErr == nil {
		writeErr = writeZipFile(writer, licensePath, "LICENSE", 0o644, sourceTime)
	}
	closeZipErr := writer.Close()
	closeOutputErr := output.Close()
	for _, candidate := range []error{writeErr, closeZipErr, closeOutputErr} {
		if candidate != nil {
			return candidate
		}
	}
	return nil
}

func writeZipFile(writer *zip.Writer, sourcePath, name string, mode os.FileMode, sourceTime time.Time) error {
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(mode)
	header.SetModTime(sourceTime.UTC())
	entry, err := writer.CreateHeader(header)
	if err != nil {
		return err
	}
	return copyFileContent(entry, sourcePath)
}

func copyFileContent(destination io.Writer, sourcePath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(destination, source)
	closeErr := source.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func copyReleaseFile(sourcePath, destinationPath string, mode os.FileMode) error {
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	return os.WriteFile(destinationPath, data, mode)
}

func writeChecksums(directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		if !entry.Type().IsRegular() || entry.Name() == "checksums.txt" {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	output, err := os.Create(filepath.Join(directory, "checksums.txt"))
	if err != nil {
		return err
	}
	buffer := bufio.NewWriter(output)
	for _, name := range names {
		digest, _, err := digestFile(filepath.Join(directory, name))
		if err != nil {
			_ = output.Close()
			return err
		}
		if _, err := fmt.Fprintf(buffer, "%s  %s\n", digest, name); err != nil {
			_ = output.Close()
			return err
		}
	}
	if err := buffer.Flush(); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

func digestFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return "", 0, copyErr
	}
	if closeErr != nil {
		return "", 0, closeErr
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

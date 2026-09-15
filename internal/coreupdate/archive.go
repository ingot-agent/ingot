package coreupdate

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	ingotrelease "github.com/ingot-agent/ingot/internal/release"
)

const (
	maxExecutableSize = 192 << 20
	maxLicenseSize    = 1 << 20
)

func extractExecutable(archivePath, directory string, artifact ingotrelease.Artifact) (string, error) {
	pattern := ".ingot-update-candidate-*"
	if artifact.GOOS == "windows" {
		pattern += ".exe"
	}
	candidate, err := os.CreateTemp(directory, pattern)
	if err != nil {
		return "", err
	}
	candidatePath := candidate.Name()
	if err := candidate.Close(); err != nil {
		_ = os.Remove(candidatePath)
		return "", err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(candidatePath)
		}
	}()
	if strings.HasSuffix(artifact.Name, ".tar.gz") {
		err = extractTarExecutable(archivePath, candidatePath, artifact.Executable)
	} else if strings.HasSuffix(artifact.Name, ".zip") {
		err = extractZipExecutable(archivePath, candidatePath, artifact.Executable)
	} else {
		err = fmt.Errorf("unsupported release archive %q", artifact.Name)
	}
	if err != nil {
		return "", err
	}
	if err := os.Chmod(candidatePath, 0o755); err != nil {
		return "", err
	}
	if err := syncFile(candidatePath); err != nil {
		return "", err
	}
	keep = true
	return candidatePath, nil
}

func extractTarExecutable(archivePath, destination, executable string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	seen := make(map[string]bool, 2)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if !allowedArchiveEntry(header.Name, executable) || header.Typeflag != tar.TypeReg || seen[header.Name] {
			return fmt.Errorf("unsafe release archive entry %q", header.Name)
		}
		seen[header.Name] = true
		if header.Name == "LICENSE" {
			if header.Size <= 0 || header.Size > maxLicenseSize {
				return fmt.Errorf("invalid license entry %q", header.Name)
			}
			continue
		}
		if header.Name != executable {
			continue
		}
		if header.Size <= 0 || header.Size > maxExecutableSize {
			return fmt.Errorf("invalid executable entry %q", header.Name)
		}
		if err := copyExecutable(destination, reader, header.Size); err != nil {
			return err
		}
	}
	if !seen[executable] {
		return fmt.Errorf("release archive does not contain %s", executable)
	}
	if !seen["LICENSE"] {
		return fmt.Errorf("release archive does not contain LICENSE")
	}
	return nil
}

func extractZipExecutable(archivePath, destination, executable string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer reader.Close()
	seen := make(map[string]bool, 2)
	for _, entry := range reader.File {
		if !allowedArchiveEntry(entry.Name, executable) || !entry.FileInfo().Mode().IsRegular() || seen[entry.Name] {
			return fmt.Errorf("unsafe release archive entry %q", entry.Name)
		}
		seen[entry.Name] = true
		if entry.Name == "LICENSE" {
			if entry.UncompressedSize64 == 0 || entry.UncompressedSize64 > maxLicenseSize {
				return fmt.Errorf("invalid license entry %q", entry.Name)
			}
			continue
		}
		if entry.Name != executable {
			continue
		}
		if entry.UncompressedSize64 == 0 || entry.UncompressedSize64 > maxExecutableSize {
			return fmt.Errorf("invalid executable entry %q", entry.Name)
		}
		input, err := entry.Open()
		if err != nil {
			return err
		}
		err = copyExecutable(destination, input, int64(entry.UncompressedSize64))
		closeErr := input.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if !seen[executable] {
		return fmt.Errorf("release archive does not contain %s", executable)
	}
	if !seen["LICENSE"] {
		return fmt.Errorf("release archive does not contain LICENSE")
	}
	return nil
}

func allowedArchiveEntry(name, executable string) bool {
	return filepath.Base(name) == name && (name == executable || name == "LICENSE")
}

func copyExecutable(destination string, source io.Reader, size int64) error {
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(output, io.LimitReader(source, size+1))
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written != size {
		return fmt.Errorf("executable entry has %d bytes, want %d", written, size)
	}
	return nil
}

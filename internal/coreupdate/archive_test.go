package coreupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ingotrelease "github.com/ingot-agent/ingot/internal/release"
)

func TestExtractZipExecutable(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "ingot-v0.3.1-windows-amd64.zip")
	writeTestZip(t, archive, map[string]zipTestEntry{
		"ingot.exe": {data: []byte("windows core"), mode: 0o755},
		"LICENSE":   {data: []byte("license"), mode: 0o644},
	})
	candidate, err := extractExecutable(archive, t.TempDir(), ingotrelease.Artifact{
		GOOS: "windows", Name: filepath.Base(archive), Executable: "ingot.exe",
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(candidate)
	if err != nil || string(data) != "windows core" {
		t.Fatalf("candidate = %q, %v", data, err)
	}
}

func TestExtractExecutableRejectsUnsafeArchives(t *testing.T) {
	t.Run("tar path", func(t *testing.T) {
		archive := filepath.Join(t.TempDir(), "unsafe.tar.gz")
		writeTestTar(t, archive, []tarTestEntry{
			{name: "../ingot", data: []byte("bad")},
			{name: "LICENSE", data: []byte("license")},
		})
		assertUnsafeArchive(t, archive, ingotrelease.Artifact{Name: "unsafe.tar.gz", Executable: "ingot"})
	})
	t.Run("zip symlink", func(t *testing.T) {
		archive := filepath.Join(t.TempDir(), "unsafe.zip")
		writeTestZip(t, archive, map[string]zipTestEntry{
			"ingot.exe": {data: []byte("target"), mode: os.ModeSymlink | 0o777},
			"LICENSE":   {data: []byte("license"), mode: 0o644},
		})
		assertUnsafeArchive(t, archive, ingotrelease.Artifact{GOOS: "windows", Name: "unsafe.zip", Executable: "ingot.exe"})
	})
	t.Run("duplicate executable", func(t *testing.T) {
		archive := filepath.Join(t.TempDir(), "duplicate.tar.gz")
		writeTestTar(t, archive, []tarTestEntry{
			{name: "ingot", data: []byte("first")},
			{name: "ingot", data: []byte("second")},
			{name: "LICENSE", data: []byte("license")},
		})
		assertUnsafeArchive(t, archive, ingotrelease.Artifact{Name: "duplicate.tar.gz", Executable: "ingot"})
	})
}

func assertUnsafeArchive(t *testing.T, archive string, artifact ingotrelease.Artifact) {
	t.Helper()
	if _, err := extractExecutable(archive, t.TempDir(), artifact); err == nil || !strings.Contains(err.Error(), "archive entry") {
		t.Fatalf("unsafe archive error = %v", err)
	}
}

type tarTestEntry struct {
	name string
	data []byte
}

func writeTestTar(t *testing.T, path string, entries []tarTestEntry) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		if err := tarWriter.WriteHeader(&tar.Header{Name: entry.name, Mode: 0o755, Size: int64(len(entry.data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

type zipTestEntry struct {
	data []byte
	mode os.FileMode
}

func writeTestZip(t *testing.T, path string, entries map[string]zipTestEntry) {
	t.Helper()
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	for name, entry := range entries {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(entry.mode)
		output, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := output.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

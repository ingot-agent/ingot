package coreupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ingot-agent/ingot/internal/buildinfo"
	ingotrelease "github.com/ingot-agent/ingot/internal/release"
)

const testCommit = "0123456789abcdef0123456789abcdef01234567"

func TestCheckLatestDoesNotResolveExecutable(t *testing.T) {
	archive := testTarArchive(t, []byte("new core"))
	client := testReleaseClient(t, "v0.3.1", archive, "")
	updater := testUpdater(client, "0.3.1-dev")
	updater.executablePath = func() (string, error) {
		t.Fatal("check resolved the executable path")
		return "", nil
	}
	result, err := updater.Run(context.Background(), Options{Check: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.UpdateAvailable || result.Updated || result.CurrentVersion != "0.3.1-dev" || result.TargetVersion != "0.3.1" {
		t.Fatalf("check result = %#v", result)
	}
}

func TestUpdateDownloadsExactAssetAndReplacesBinary(t *testing.T) {
	binary := []byte("new core")
	archive := testTarArchive(t, binary)
	client := testReleaseClient(t, "v0.3.1", archive, "")
	updater := testUpdater(client, "0.3.1-dev")
	directory := t.TempDir()
	target := filepath.Join(directory, "ingot")
	if err := os.WriteFile(target, []byte("old core"), 0o755); err != nil {
		t.Fatal(err)
	}
	updater.executablePath = func() (string, error) { return target, nil }
	updater.verifyCandidate = func(_ context.Context, path string) (buildinfo.Info, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return buildinfo.Info{}, err
		}
		if !bytes.Equal(data, binary) {
			t.Fatalf("candidate = %q", data)
		}
		return buildinfo.Info{CoreVersion: "0.3.1", Official: true, Revision: testCommit, Target: "linux/amd64"}, nil
	}
	updater.replaceBinary = func(candidate, destination string) error {
		data, err := os.ReadFile(candidate)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, data, 0o755)
	}
	updater.lockBinary = func(context.Context, string) (func(), error) { return func() {}, nil }
	result, err := updater.Run(context.Background(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Updated || result.Executable != target {
		t.Fatalf("update result = %#v", result)
	}
	installed, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(installed, binary) {
		t.Fatalf("installed binary = %q", installed)
	}
}

func TestChecksumMismatchLeavesBinaryUntouched(t *testing.T) {
	archive := testTarArchive(t, []byte("new core"))
	client := testReleaseClient(t, "v0.3.1", archive, strings.Repeat("f", 64))
	updater := testUpdater(client, "0.3.1-dev")
	directory := t.TempDir()
	target := filepath.Join(directory, "ingot")
	if err := os.WriteFile(target, []byte("old core"), 0o755); err != nil {
		t.Fatal(err)
	}
	updater.executablePath = func() (string, error) { return target, nil }
	updater.lockBinary = func(context.Context, string) (func(), error) { return func() {}, nil }
	replaced := false
	updater.replaceBinary = func(string, string) error { replaced = true; return nil }
	if _, err := updater.Run(context.Background(), Options{}); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("checksum error = %v", err)
	}
	if replaced {
		t.Fatal("checksum failure replaced the binary")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "old core" {
		t.Fatalf("target after failure = %q, %v", data, err)
	}
}

func TestExactDowngradeRequiresForce(t *testing.T) {
	archive := testTarArchive(t, []byte("old release"))
	client := testReleaseClient(t, "v0.3.0", archive, "")
	updater := testUpdater(client, "0.3.1")
	if _, err := updater.Run(context.Background(), Options{Version: "v0.3.0", Check: true}); err == nil || !strings.Contains(err.Error(), "older") {
		t.Fatalf("downgrade error = %v", err)
	}
	updater.executablePath = func() (string, error) { return filepath.Join(t.TempDir(), "ingot"), nil }
	updater.lockBinary = func(context.Context, string) (func(), error) { return func() {}, nil }
	updater.verifyCandidate = func(context.Context, string) (buildinfo.Info, error) {
		return buildinfo.Info{CoreVersion: "0.3.0", Official: true, Revision: testCommit, Target: "linux/amd64"}, nil
	}
	updater.replaceBinary = func(string, string) error { return nil }
	if _, err := updater.Run(context.Background(), Options{Version: "0.3.0", Force: true}); err != nil {
		t.Fatal(err)
	}
}

func TestLatestRejectsPrereleaseManifest(t *testing.T) {
	archive := testTarArchive(t, []byte("candidate"))
	client := testReleaseClient(t, "v0.3.2-rc.1", archive, "")
	updater := testUpdater(client, "0.3.1")
	if _, err := updater.Run(context.Background(), Options{Check: true}); err == nil || !strings.Contains(err.Error(), "prerelease") {
		t.Fatalf("prerelease latest error = %v", err)
	}
}

func TestCandidateIdentityMismatchDoesNotReplaceBinary(t *testing.T) {
	archive := testTarArchive(t, []byte("candidate"))
	updater := testUpdater(testReleaseClient(t, "v0.3.1", archive, ""), "0.3.1-dev")
	directory := t.TempDir()
	target := filepath.Join(directory, "ingot")
	if err := os.WriteFile(target, []byte("old core"), 0o755); err != nil {
		t.Fatal(err)
	}
	updater.executablePath = func() (string, error) { return target, nil }
	updater.lockBinary = func(context.Context, string) (func(), error) { return func() {}, nil }
	updater.verifyCandidate = func(context.Context, string) (buildinfo.Info, error) {
		return buildinfo.Info{CoreVersion: "0.3.1", Official: false, Revision: testCommit, Target: "linux/amd64"}, nil
	}
	replaced := false
	updater.replaceBinary = func(string, string) error { replaced = true; return nil }
	if _, err := updater.Run(context.Background(), Options{}); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("identity error = %v", err)
	}
	if replaced {
		t.Fatal("identity mismatch replaced the binary")
	}
}

func TestReplacementFailureCleansCandidateAndLeavesTarget(t *testing.T) {
	archive := testTarArchive(t, []byte("candidate"))
	updater := testUpdater(testReleaseClient(t, "v0.3.1", archive, ""), "0.3.1-dev")
	directory := t.TempDir()
	target := filepath.Join(directory, "ingot")
	if err := os.WriteFile(target, []byte("old core"), 0o755); err != nil {
		t.Fatal(err)
	}
	updater.executablePath = func() (string, error) { return target, nil }
	updater.lockBinary = func(context.Context, string) (func(), error) { return func() {}, nil }
	updater.verifyCandidate = func(context.Context, string) (buildinfo.Info, error) {
		return buildinfo.Info{CoreVersion: "0.3.1", Official: true, Revision: testCommit, Target: "linux/amd64"}, nil
	}
	updater.replaceBinary = func(string, string) error { return errors.New("replacement denied") }
	if _, err := updater.Run(context.Background(), Options{}); err == nil || !strings.Contains(err.Error(), "replacement denied") {
		t.Fatalf("replacement error = %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "old core" {
		t.Fatalf("target after replacement failure = %q, %v", data, err)
	}
	candidates, err := filepath.Glob(filepath.Join(directory, ".ingot-update-candidate-*"))
	if err != nil || len(candidates) != 0 {
		t.Fatalf("staged candidates after failure = %v, %v", candidates, err)
	}
}

func TestReplaceExecutableKeepsCandidateAndCleansBackup(t *testing.T) {
	directory := t.TempDir()
	destination := filepath.Join(directory, "ingot")
	if runtime.GOOS == "windows" {
		destination += ".exe"
	}
	candidate := filepath.Join(directory, "candidate")
	if runtime.GOOS == "windows" {
		candidate += ".exe"
	}
	if err := os.WriteFile(destination, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceExecutable(candidate, destination); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "new" {
		t.Fatalf("destination = %q, %v", data, err)
	}
	if err := cleanupPreviousExecutable(destination); err != nil {
		t.Fatal(err)
	}
}

func testUpdater(client *http.Client, currentVersion string) *Updater {
	updater := New()
	updater.BaseURL = "https://release.test/releases"
	updater.HTTPClient = client
	updater.Current = buildinfo.Info{CoreVersion: currentVersion, Target: "linux/amd64"}
	updater.GOOS, updater.GOARCH = "linux", "amd64"
	return updater
}

func testReleaseClient(t *testing.T, tag string, archive []byte, digestOverride string) *http.Client {
	t.Helper()
	digest := sha256.Sum256(archive)
	digestText := hex.EncodeToString(digest[:])
	if digestOverride != "" {
		digestText = digestOverride
	}
	version := strings.TrimPrefix(tag, "v")
	assetName := "ingot-" + tag + "-linux-amd64.tar.gz"
	manifest := ingotrelease.Manifest{
		SchemaVersion: ingotrelease.ManifestSchemaVersion, Tag: tag, Version: version, Commit: testCommit,
	}
	for _, target := range ingotrelease.CoreTargets {
		extension := ".tar.gz"
		executable := "ingot"
		if target.GOOS == "windows" {
			extension = ".zip"
			executable = "ingot.exe"
		}
		manifest.Artifacts = append(manifest.Artifacts, ingotrelease.Artifact{
			GOOS: target.GOOS, GOARCH: target.GOARCH,
			Name:   "ingot-" + tag + "-" + target.GOOS + "-" + target.GOARCH + extension,
			SHA256: digestText, Size: int64(len(archive)), Executable: executable,
		})
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var data []byte
		contentType := "application/octet-stream"
		switch request.URL.Path {
		case "/releases/latest/download/release-manifest.json", "/releases/download/" + tag + "/release-manifest.json":
			data = manifestData
			contentType = "application/json"
		case "/releases/download/" + tag + "/" + assetName:
			data = archive
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Header: make(http.Header), Body: io.NopCloser(strings.NewReader("not found")), Request: request}, nil
		}
		header := make(http.Header)
		header.Set("Content-Type", contentType)
		header.Set("Content-Length", fmt.Sprint(len(data)))
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: header, ContentLength: int64(len(data)), Body: io.NopCloser(bytes.NewReader(data)), Request: request}, nil
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func testTarArchive(t *testing.T, binary []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "ingot", Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(binary); err != nil {
		t.Fatal(err)
	}
	license := []byte("license")
	if err := tarWriter.WriteHeader(&tar.Header{Name: "LICENSE", Mode: 0o644, Size: int64(len(license)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(license); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

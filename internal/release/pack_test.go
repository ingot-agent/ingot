package release

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPackCoreReleaseIsDeterministic(t *testing.T) {
	input := t.TempDir()
	for _, target := range CoreTargets {
		name := "ingot-" + target.GOOS + "-" + target.GOARCH
		if target.GOOS == "windows" {
			name += ".exe"
		}
		if err := os.WriteFile(filepath.Join(input, name), []byte("binary:"+name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	license := filepath.Join(root, "LICENSE")
	installSH := filepath.Join(root, "install.sh")
	installPS1 := filepath.Join(root, "install.ps1")
	for path, data := range map[string]string{license: "license\n", installSH: "#!/bin/sh\n", installPS1: "Write-Host ingot\n"} {
		if err := os.WriteFile(path, []byte(data), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pack := func(output string) Manifest {
		manifest, err := PackCoreRelease(PackOptions{
			Version: "v0.3.1", Commit: "0123456789abcdef0123456789abcdef01234567",
			SourceTime: time.Unix(1_800_000_000, 0), InputDirectory: input, OutputDirectory: output,
			LicensePath: license, InstallSHPath: installSH, InstallPS1Path: installPS1,
		})
		if err != nil {
			t.Fatal(err)
		}
		return manifest
	}
	firstDir, secondDir := filepath.Join(root, "first"), filepath.Join(root, "second")
	first, second := pack(firstDir), pack(secondDir)
	if len(first.Artifacts) != len(CoreTargets) || len(second.Artifacts) != len(CoreTargets) {
		t.Fatalf("artifact count = %d, %d", len(first.Artifacts), len(second.Artifacts))
	}
	firstChecksums, err := os.ReadFile(filepath.Join(firstDir, "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	secondChecksums, err := os.ReadFile(filepath.Join(secondDir, "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(firstChecksums) != string(secondChecksums) {
		t.Fatalf("release packaging is not deterministic:\n%s\n%s", firstChecksums, secondChecksums)
	}
	checksums := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(firstChecksums)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			t.Fatalf("invalid checksum line %q", scanner.Text())
		}
		checksums[fields[1]] = fields[0]
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(firstDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || entry.Name() == "checksums.txt" {
			continue
		}
		digest, _, err := digestFile(filepath.Join(firstDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if checksums[entry.Name()] != digest {
			t.Fatalf("checksum for %s = %q, want %q", entry.Name(), checksums[entry.Name()], digest)
		}
		delete(checksums, entry.Name())
	}
	if len(checksums) != 0 {
		t.Fatalf("checksums reference missing assets: %v", checksums)
	}
	manifestData, err := os.ReadFile(filepath.Join(firstDir, "release-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseManifest(manifestData)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Artifacts) != len(CoreTargets) {
		t.Fatalf("parsed artifact count = %d", len(parsed.Artifacts))
	}
}

func TestPackCoreReleaseRejectsNonEmptyOutput(t *testing.T) {
	output := t.TempDir()
	if err := os.WriteFile(filepath.Join(output, "stale-asset"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := PackCoreRelease(PackOptions{
		Version: "v0.3.1", Commit: "0123456789abcdef0123456789abcdef01234567",
		SourceTime: time.Unix(1_800_000_000, 0), OutputDirectory: output,
	})
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("non-empty output error = %v", err)
	}
}

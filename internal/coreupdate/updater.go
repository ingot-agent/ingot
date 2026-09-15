// Package coreupdate checks and replaces the current ingot core binary.
package coreupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ingot-agent/ingot/internal/buildinfo"
	ingotrelease "github.com/ingot-agent/ingot/internal/release"
	"golang.org/x/mod/semver"
)

const officialReleaseBaseURL = "https://github.com/ingot-agent/ingot/releases"

type Options struct {
	Check   bool
	Version string
	Force   bool
}

type Result struct {
	CurrentVersion  string `json:"current_version"`
	TargetVersion   string `json:"target_version"`
	UpdateAvailable bool   `json:"update_available"`
	Updated         bool   `json:"updated"`
	Executable      string `json:"executable,omitempty"`
}

// Updater has injectable boundaries so network and replacement failures can
// be tested without modifying the test executable.
type Updater struct {
	BaseURL         string
	HTTPClient      *http.Client
	Current         buildinfo.Info
	GOOS            string
	GOARCH          string
	executablePath  func() (string, error)
	verifyCandidate func(context.Context, string) (buildinfo.Info, error)
	replaceBinary   func(string, string) error
	lockBinary      func(context.Context, string) (func(), error)
}

func New() *Updater {
	return &Updater{
		BaseURL:         officialReleaseBaseURL,
		HTTPClient:      &http.Client{Timeout: 5 * time.Minute},
		Current:         buildinfo.Current(),
		GOOS:            runtime.GOOS,
		GOARCH:          runtime.GOARCH,
		executablePath:  currentExecutablePath,
		verifyCandidate: inspectCandidate,
		replaceBinary:   replaceExecutable,
		lockBinary:      acquireUpdateLock,
	}
}

func (updater *Updater) Run(ctx context.Context, options Options) (Result, error) {
	if updater == nil {
		updater = New()
	}
	updater.defaults()
	currentTag, err := canonicalTag(updater.Current.CoreVersion)
	if err != nil {
		return Result{}, fmt.Errorf("current core version: %w", err)
	}
	requestedTag := ""
	if options.Version != "" {
		requestedTag, err = canonicalTag(options.Version)
		if err != nil {
			return Result{}, fmt.Errorf("requested core version: %w", err)
		}
	}
	manifest, err := updater.resolveManifest(ctx, requestedTag)
	if err != nil {
		return Result{}, err
	}
	comparison := semver.Compare(manifest.Tag, currentTag)
	result := Result{
		CurrentVersion:  strings.TrimPrefix(currentTag, "v"),
		TargetVersion:   manifest.Version,
		UpdateAvailable: comparison != 0,
	}
	if comparison == 0 && !options.Force {
		return result, nil
	}
	if comparison < 0 && !options.Force {
		if requestedTag == "" {
			result.UpdateAvailable = false
			return result, nil
		}
		return Result{}, fmt.Errorf("target version %s is older than current version %s; pass --force to downgrade", manifest.Tag, currentTag)
	}
	if options.Force && comparison <= 0 && requestedTag == "" {
		return Result{}, fmt.Errorf("--force requires --version when reinstalling or downgrading")
	}
	if options.Check {
		return result, nil
	}

	executable, err := updater.executablePath()
	if err != nil {
		return Result{}, fmt.Errorf("locate current executable: %w", err)
	}
	result.Executable = executable
	releaseLock, err := updater.lockBinary(ctx, executable)
	if err != nil {
		return Result{}, fmt.Errorf("lock core update: %w", err)
	}
	defer releaseLock()

	artifact, err := manifest.ArtifactFor(updater.GOOS, updater.GOARCH)
	if err != nil {
		return Result{}, err
	}
	directory := filepath.Dir(executable)
	archive, err := os.CreateTemp(directory, ".ingot-update-archive-*")
	if err != nil {
		return Result{}, fmt.Errorf("create update staging file next to %s: %w", executable, err)
	}
	archivePath := archive.Name()
	_ = archive.Close()
	defer os.Remove(archivePath)
	assetURL := strings.TrimRight(updater.BaseURL, "/") + "/download/" + url.PathEscape(manifest.Tag) + "/" + url.PathEscape(artifact.Name)
	if err := updater.downloadAsset(ctx, assetURL, archivePath, artifact); err != nil {
		return Result{}, err
	}
	candidatePath, err := extractExecutable(archivePath, directory, artifact)
	if err != nil {
		return Result{}, err
	}
	defer os.Remove(candidatePath)
	candidate, err := updater.verifyCandidate(ctx, candidatePath)
	if err != nil {
		return Result{}, fmt.Errorf("verify candidate core: %w", err)
	}
	wantTarget := updater.GOOS + "/" + updater.GOARCH
	if candidate.CoreVersion != manifest.Version || !candidate.Official || candidate.Target != wantTarget || candidate.Revision != manifest.Commit || candidate.Modified {
		return Result{}, fmt.Errorf("candidate identity mismatch: got version=%q official=%t revision=%q modified=%t target=%q", candidate.CoreVersion, candidate.Official, candidate.Revision, candidate.Modified, candidate.Target)
	}
	if err := updater.replaceBinary(candidatePath, executable); err != nil {
		return Result{}, fmt.Errorf("replace core executable %s: %w", executable, err)
	}
	result.Updated = true
	return result, nil
}

func (updater *Updater) defaults() {
	defaults := New()
	if updater.BaseURL == "" {
		updater.BaseURL = defaults.BaseURL
	}
	if updater.HTTPClient == nil {
		updater.HTTPClient = defaults.HTTPClient
	}
	if updater.Current.CoreVersion == "" {
		updater.Current = defaults.Current
	}
	if updater.GOOS == "" {
		updater.GOOS = defaults.GOOS
	}
	if updater.GOARCH == "" {
		updater.GOARCH = defaults.GOARCH
	}
	if updater.executablePath == nil {
		updater.executablePath = defaults.executablePath
	}
	if updater.verifyCandidate == nil {
		updater.verifyCandidate = defaults.verifyCandidate
	}
	if updater.replaceBinary == nil {
		updater.replaceBinary = defaults.replaceBinary
	}
	if updater.lockBinary == nil {
		updater.lockBinary = defaults.lockBinary
	}
}

func canonicalTag(value string) (string, error) {
	tag := value
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	if strings.Contains(tag, "+") || !semver.IsValid(tag) || semver.Canonical(tag) != tag {
		return "", fmt.Errorf("expected canonical SemVer, got %q", value)
	}
	return tag, nil
}

func (updater *Updater) resolveManifest(ctx context.Context, requestedTag string) (ingotrelease.Manifest, error) {
	manifestURL := strings.TrimRight(updater.BaseURL, "/") + "/latest/download/release-manifest.json"
	if requestedTag != "" {
		manifestURL = strings.TrimRight(updater.BaseURL, "/") + "/download/" + url.PathEscape(requestedTag) + "/release-manifest.json"
	}
	data, err := updater.fetch(ctx, manifestURL, ingotrelease.MaxManifestSize)
	if err != nil {
		return ingotrelease.Manifest{}, fmt.Errorf("download release manifest: %w", err)
	}
	manifest, err := ingotrelease.ParseManifest(data)
	if err != nil {
		return ingotrelease.Manifest{}, err
	}
	if requestedTag != "" && manifest.Tag != requestedTag {
		return ingotrelease.Manifest{}, fmt.Errorf("release manifest tag %s does not match requested tag %s", manifest.Tag, requestedTag)
	}
	if requestedTag == "" && semver.Prerelease(manifest.Tag) != "" {
		return ingotrelease.Manifest{}, fmt.Errorf("latest stable manifest resolved to prerelease %s", manifest.Tag)
	}
	return manifest, nil
}

func (updater *Updater) fetch(ctx context.Context, source string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "ingot/"+updater.Current.CoreVersion)
	response, err := updater.HTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", source, response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response from %s exceeds %d bytes", source, limit)
	}
	return data, nil
}

func (updater *Updater) downloadAsset(ctx context.Context, source, destination string, artifact ingotrelease.Artifact) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "ingot/"+updater.Current.CoreVersion)
	response, err := updater.HTTPClient.Do(request)
	if err != nil {
		return fmt.Errorf("download %s: %w", artifact.Name, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", artifact.Name, response.Status)
	}
	if response.ContentLength > artifact.Size || response.ContentLength > ingotrelease.MaxArchiveSize {
		return fmt.Errorf("download %s: unexpected content length %d", artifact.Name, response.ContentLength)
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, artifact.Size+1))
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("download %s: %w", artifact.Name, copyErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if written != artifact.Size {
		return fmt.Errorf("download %s: got %d bytes, want %d", artifact.Name, written, artifact.Size)
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != artifact.SHA256 {
		return fmt.Errorf("download %s: SHA-256 %s, want %s", artifact.Name, actual, artifact.SHA256)
	}
	return syncFile(destination)
}

func inspectCandidate(ctx context.Context, path string) (buildinfo.Info, error) {
	command := exec.CommandContext(ctx, path, "version")
	stdout := limitedBuffer{limit: ingotrelease.MaxManifestSize}
	stderr := limitedBuffer{limit: ingotrelease.MaxManifestSize}
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return buildinfo.Info{}, fmt.Errorf("run %s version: %w: %s", path, err, strings.TrimSpace(stderr.String()))
	}
	var info buildinfo.Info
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		return buildinfo.Info{}, fmt.Errorf("decode candidate version: %w", err)
	}
	return info, nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (buffer *limitedBuffer) Write(data []byte) (int, error) {
	remaining := buffer.limit - buffer.Len()
	if remaining <= 0 {
		return 0, fmt.Errorf("candidate output exceeds %d bytes", buffer.limit)
	}
	if len(data) > remaining {
		_, _ = buffer.Buffer.Write(data[:remaining])
		return remaining, fmt.Errorf("candidate output exceeds %d bytes", buffer.limit)
	}
	return buffer.Buffer.Write(data)
}

func currentExecutablePath() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Abs(path)
}

// CleanupPrevious removes a Windows backup left while replacing the running
// executable. It is a no-op on other platforms and on ordinary installations.
func CleanupPrevious() error {
	path, err := currentExecutablePath()
	if err != nil {
		return err
	}
	return cleanupPreviousExecutable(path)
}

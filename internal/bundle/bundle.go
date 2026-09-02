// Package bundle locates and materializes the official ingot plugin set.
//
// The official plugins live in this repository under plugins/ and are
// distributed next to the ingot binary (for example under
// share/ingot/plugins for an FHS install, or as a sibling plugins/ directory
// for an in-repo dev build). `ingot init` locates that distribution
// directory, materializes it under <home>/bundled-plugins/, and declares the
// plugins in plugins.toml as local dev sources, so a freshly initialized home
// can resolve and build its first runtime image without fetching the plugin
// modules themselves.
package bundle

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// BundledDirectory is the home-relative directory that holds the materialized
// official plugin sources. The path entries written by init reference it.
const BundledDirectory = "bundled-plugins"

// markerName is a file inside BundledDirectory recording the content digest
// of the distribution plugin set used for the last materialization. It is
// used to skip rewriting when the sources have not changed.
const markerName = ".ingot-bundle-digest"

// Profile is one official default plugin set shipped with ingot. Plugins
// holds plugin directory names (relative to the plugins distribution
// directory) in Direct Plugin Order.
type Profile struct {
	Name    string
	Plugins []string
}

var profiles = map[string]*Profile{
	"default": {
		Name: "default",
		// Runtime skeleton first, then providers and adapters, agent and CLI
		// last so that MANY aggregation and creation order are stable and
		// readable.
		Plugins: []string{
			"asset-local",
			"http-default",
			"model-openai-compatible",
			"model-runtime",
			"tool-runtime",
			"tool-shell",
			"tool-fs",
			"tool-ask",
			"interceptor-approval",
			"filesystem-local",
			"prompt-default",
			"session-sqlite",
			"agent-default",
			"app-cli",
		},
	},
	"minimal": {
		Name: "minimal",
		// Exactly the runtime skeleton plus one model provider and the HTTP
		// client it requires. No tools, no filesystem, no approvals.
		Plugins: []string{
			"asset-local",
			"http-default",
			"model-openai-compatible",
			"model-runtime",
			"tool-runtime",
			"prompt-default",
			"session-sqlite",
			"agent-default",
			"app-cli",
		},
	},
}

// Entry is one materialized official plugin.
type Entry struct {
	// Directory is the plugin directory name under both the distribution
	// directory and <home>/bundled-plugins/.
	Directory string
	// Module is the canonical module path read from the plugin's go.mod.
	Module string
	// Name is the manifest short name from ingot.plugin.toml.
	Name string
}

// LookupProfile returns the named official profile.
func LookupProfile(name string) (*Profile, error) {
	if name == "" {
		name = "default"
	}
	profile, ok := profiles[name]
	if !ok {
		names := make([]string, 0, len(profiles))
		for candidate := range profiles {
			names = append(names, candidate)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("unknown profile %q (available: %s)", name, strings.Join(names, ", "))
	}
	return profile, nil
}

// Locate returns the distribution plugin directory. explicit (the --bundle
// flag) wins; otherwise the candidates derived from the executable location
// are probed in order. A directory is accepted when it contains every
// official plugin directory with a go.mod and ingot.plugin.toml.
//
// Candidates for an installed layout (binary at <prefix>/bin/ingot,
// sources at <prefix>/share/ingot/plugins) and for a dev checkout (binary at
// the repository root, sources at ./plugins) are both covered.
func Locate(explicit string) (string, error) {
	candidates := make([]string, 0, 4)
	if explicit != "" {
		candidates = append(candidates, explicit)
	} else if executable, err := os.Executable(); err == nil {
		executable, err = filepath.EvalSymlinks(executable)
		if err != nil {
			executable, _ = os.Executable()
		}
		binDirectory := filepath.Dir(executable)
		candidates = append(candidates,
			filepath.Join(binDirectory, "plugins"),                   // in-repo dev binary / dist layout
			filepath.Join(binDirectory, "share", "ingot", "plugins"), // FHS layout, <prefix>/bin
			filepath.Join(binDirectory, "..", "plugins"),             // binary under bin/, sources as sibling
			filepath.Join(binDirectory, "..", "share", "ingot", "plugins"),
		)
	}
	for _, candidate := range candidates {
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if validDistribution(absolute) {
			return absolute, nil
		}
	}
	if explicit != "" {
		return "", fmt.Errorf("plugin bundle %s is not a valid official plugin set (expected one directory per official plugin with go.mod and ingot.plugin.toml)", explicit)
	}
	return "", fmt.Errorf("official plugin bundle not found; run scripts/install.sh, or pass --bundle PATH (e.g. --bundle ./plugins)")
}

// validDistribution reports whether directory contains every official plugin
// directory.
func validDistribution(directory string) bool {
	for _, profile := range profiles {
		for _, pluginDirectory := range profile.Plugins {
			if !validPluginDirectory(filepath.Join(directory, pluginDirectory)) {
				return false
			}
		}
	}
	return true
}

func validPluginDirectory(directory string) bool {
	for _, required := range []string{"go.mod", "ingot.plugin.toml"} {
		info, err := os.Stat(filepath.Join(directory, required))
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}

// Materialize ensures the complete distribution plugin set exists under
// <homeRoot>/bundled-plugins/ and returns the identity entries for the
// profile's plugins in declaration order.
//
// Materialization is idempotent: when the content digest matches the marker
// and every plugin directory exists, files are left untouched. A changed
// distribution (for example after upgrading ingot) causes a full rewrite of
// the directory, which is managed entirely by ingot.
func Materialize(sourceDir, homeRoot string, profile *Profile) ([]Entry, error) {
	destRoot := filepath.Join(homeRoot, BundledDirectory)
	digest, err := sourceDigest(sourceDir)
	if err != nil {
		return nil, err
	}
	if !staleMarker(destRoot, digest, profile) {
		entries, err := readEntries(destRoot, profile)
		if err == nil {
			return entries, nil
		}
		// Fall through to a full rewrite when the on-disk state is incomplete.
	}
	if err := rewrite(sourceDir, destRoot, homeRoot, digest); err != nil {
		return nil, err
	}
	return readEntries(destRoot, profile)
}

// staleMarker reports whether destRoot is missing, has a different digest, or
// is missing any of the profile's plugin directories.
func staleMarker(destRoot, digest string, profile *Profile) bool {
	data, err := os.ReadFile(filepath.Join(destRoot, markerName))
	if err != nil || strings.TrimSpace(string(data)) != digest {
		return true
	}
	for _, directory := range profile.Plugins {
		info, statErr := os.Stat(filepath.Join(destRoot, directory))
		if statErr != nil || !info.IsDir() {
			return true
		}
	}
	return false
}

// sourceDigest hashes the plugin distribution directory content in
// deterministic (logical path, bytes) order. Metadata directories (.git,
// .idea, ...) are excluded so they never break reproducibility.
func sourceDigest(sourceDir string) (string, error) {
	var paths []string
	err := filepath.WalkDir(sourceDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != sourceDir && excludedSourceDirectories[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		relative, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return "", err
		}
		_, _ = hash.Write([]byte(filepath.ToSlash(relative)))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil)), nil
}

// excludedSourceDirectories mirrors the builder's DevSourceDigest exclusions
// plus editor and VCS metadata that must not enter the bundle identity.
var excludedSourceDirectories = map[string]bool{
	".git": true, ".hg": true, ".svn": true, ".idea": true, ".vscode": true,
}

// rewrite rebuilds destRoot from sourceDir in a temporary sibling directory
// and atomically swaps it into place.
func rewrite(sourceDir, destRoot, homeRoot, digest string) error {
	if err := os.MkdirAll(homeRoot, 0o700); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(homeRoot, ".bundled-plugins-")
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(temporary)
		}
	}()
	if err := copySources(sourceDir, temporary); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(temporary, markerName), []byte(digest+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.RemoveAll(destRoot); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(temporary, destRoot); err != nil {
		return err
	}
	keep = true
	return nil
}

// copySources copies every regular file of sourceDir into dest, preserving
// the relative directory structure. Hidden or metadata directories (.git,
// .idea, ...) are skipped; only regular files are copied.
func copySources(sourceDir, dest string) error {
	return filepath.WalkDir(sourceDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != sourceDir && excludedSourceDirectories[entry.Name()] {
				return filepath.SkipDir
			}
			if path != sourceDir {
				relative, err := filepath.Rel(sourceDir, path)
				if err != nil {
					return err
				}
				return os.MkdirAll(filepath.Join(dest, relative), 0o700)
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("bundle source %s is not a regular file", path)
		}
		relative, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dest, relative), data, 0o600)
	})
}

// readEntries returns the identity entries of the profile plugins by parsing
// their materialized go.mod and ingot.plugin.toml.
func readEntries(destRoot string, profile *Profile) ([]Entry, error) {
	entries := make([]Entry, len(profile.Plugins))
	for i, directory := range profile.Plugins {
		entry, err := readEntry(filepath.Join(destRoot, directory))
		if err != nil {
			return nil, fmt.Errorf("bundled plugin %s: %w", directory, err)
		}
		entry.Directory = directory
		entries[i] = entry
	}
	return entries, nil
}

package builder

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/mod/module"
)

var excludedSourceDirectories = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
}

type sourceDigestEntry struct {
	logical string
	path    string
	mode    fs.FileMode
}

// DevSourceDigest computes the exact INGOT-DEV-SOURCE-DIGEST-V1 content
// identity for a local plugin module.
func DevSourceDigest(root string) (string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	resolvedRoot, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", &Error{Code: "INGOT-DEV-SOURCE-ROOT", Path: absolute, Err: err}
	}
	absolute = filepath.Clean(resolvedRoot)
	for _, required := range []string{"go.mod", "ingot.plugin.toml"} {
		info, statErr := os.Stat(filepath.Join(absolute, required))
		if statErr != nil || !info.Mode().IsRegular() {
			if statErr == nil {
				statErr = fmt.Errorf("not a regular file")
			}
			return "", &Error{Code: "INGOT-DEV-SOURCE-REQUIRED", Path: absolute, Field: required, Err: statErr}
		}
	}

	var entries []sourceDigestEntry
	err = filepath.WalkDir(absolute, func(current string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == absolute {
			return nil
		}
		if item.IsDir() && excludedSourceDirectories[item.Name()] {
			return filepath.SkipDir
		}
		relative, relErr := filepath.Rel(absolute, current)
		if relErr != nil {
			return relErr
		}
		logical := filepath.ToSlash(relative)
		if !utf8.ValidString(logical) {
			return &Error{Code: "INGOT-DEV-SOURCE-PATH-UTF8", Path: current}
		}
		info, infoErr := item.Info()
		if infoErr != nil {
			return infoErr
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() && info.Mode()&fs.ModeSymlink == 0 {
			return &Error{Code: "INGOT-DEV-SOURCE-SPECIAL", Path: current, Actual: info.Mode().String()}
		}
		entries = append(entries, sourceDigestEntry{logical: logical, path: current, mode: info.Mode()})
		return nil
	})
	if err != nil {
		return "", err
	}
	// filepath.WalkDir is lexical on supported hosts, but explicitly sort by the
	// normative UTF-8 logical path bytes.
	sortEntries(entries)

	hash := sha256.New()
	_, _ = io.WriteString(hash, "INGOT-DEV-SOURCE-DIGEST-V1\n")
	writer := bufio.NewWriter(hash)
	for _, item := range entries {
		if item.mode&fs.ModeSymlink != 0 {
			target, readErr := os.Readlink(item.path)
			if readErr != nil {
				return "", readErr
			}
			if filepath.IsAbs(target) {
				return "", &Error{Code: "INGOT-DEV-SOURCE-SYMLINK-ABSOLUTE", Path: item.path, Actual: target}
			}
			canonicalTarget := filepath.ToSlash(filepath.Clean(target))
			if !utf8.ValidString(canonicalTarget) {
				return "", &Error{Code: "INGOT-DEV-SOURCE-SYMLINK-UTF8", Path: item.path}
			}
			resolved, evalErr := filepath.EvalSymlinks(item.path)
			if evalErr != nil {
				return "", &Error{Code: "INGOT-DEV-SOURCE-SYMLINK", Path: item.path, Err: evalErr}
			}
			if err := ensureInsideSourceRoot(absolute, resolved); err != nil {
				return "", &Error{Code: "INGOT-DEV-SOURCE-SYMLINK-ESCAPE", Path: item.path, Actual: target, Err: err}
			}
			if pointsIntoExcludedDirectory(absolute, resolved) {
				return "", &Error{Code: "INGOT-DEV-SOURCE-SYMLINK-EXCLUDED", Path: item.path, Actual: target}
			}
			writer.WriteByte('L')
			writeDigestField(writer, []byte(item.logical))
			writeDigestField(writer, []byte(canonicalTarget))
			continue
		}
		data, readErr := os.ReadFile(item.path)
		if readErr != nil {
			return "", readErr
		}
		writer.WriteByte('F')
		writeDigestField(writer, []byte(item.logical))
		writeDigestField(writer, data)
	}
	if err := writer.Flush(); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func sortEntries(entries []sourceDigestEntry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].logical < entries[j].logical })
}

func ensureInsideSourceRoot(root, candidate string) error {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s is outside %s", candidate, root)
	}
	return nil
}

func pointsIntoExcludedDirectory(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return true
	}
	for _, segment := range strings.Split(filepath.ToSlash(relative), "/") {
		if excludedSourceDirectories[segment] {
			return true
		}
	}
	return false
}

func writeDigestField(writer io.Writer, data []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(data)))
	_, _ = writer.Write(size[:])
	_, _ = writer.Write(data)
}

// SyntheticVersion returns the deterministic version used by the generated
// root module for a local development replacement.
func SyntheticVersion(modulePath string) (string, error) {
	if err := module.CheckPath(modulePath); err != nil {
		return "", err
	}
	_, major, ok := module.SplitPathVersion(modulePath)
	if !ok {
		return "", fmt.Errorf("invalid semantic import version in %q", modulePath)
	}
	if strings.HasPrefix(major, "/v") {
		return strings.TrimPrefix(major, "/") + ".0.0", nil
	}
	return "v0.0.0", nil
}

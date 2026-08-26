package builder

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// devSourceCopyPrefix is the staging directory (relative to the build root
// staging area) that receives faithful copies of every dev-source plugin.
// The restored root module replaces them via relative locators, so the
// compiled artifact embeds no machine-specific absolute path.
const devSourceCopyPrefix = "dev"

// copyDevSources copies every dev replacement source into the staging area
// and returns the relative replace locators (relative to rootDirectory) and
// the absolute staging paths, keyed by module path. The content identity was
// already verified against the locked digests before the copy; this stage
// only produces compile inputs, so VCS and editor metadata directories are
// skipped and symlinks are recreated verbatim.
func copyDevSources(lock *Lock, rootDirectory, stagingDirectory string) (map[string]string, map[string]string, error) {
	if len(lock.Replacements) == 0 {
		return map[string]string{}, map[string]string{}, nil
	}
	locators := make(map[string]string, len(lock.Replacements))
	absolute := make(map[string]string, len(lock.Replacements))
	for _, replacement := range lock.Replacements {
		directory := devSourceDirName(replacement.ModulePath)
		target := filepath.Join(stagingDirectory, devSourceCopyPrefix, directory)
		if err := copyDevSourceTree(replacement.DevPath, target); err != nil {
			return nil, nil, &Error{Code: "INGOT-BUILD-DEV-COPY", Plugin: replacement.ModulePath, Path: replacement.DevPath, Err: err}
		}
		relative, err := filepath.Rel(rootDirectory, target)
		if err != nil {
			return nil, nil, err
		}
		locators[replacement.ModulePath] = filepath.ToSlash(relative)
		absolute[replacement.ModulePath] = target
	}
	return locators, absolute, nil
}

// devSourceDirName derives a unique, deterministic directory name for a
// module path inside the staging area.
func devSourceDirName(modulePath string) string {
	return strings.ReplaceAll(modulePath, "/", "_")
}

// copyDevSourceTree mirrors a local plugin module into dest: regular files
// are copied byte-for-byte, symlinks are recreated with their (relative)
// target, and VCS metadata directories are skipped.
func copyDevSourceTree(source, dest string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != source && excludedSourceDirectories[entry.Name()] {
				return filepath.SkipDir
			}
			relative, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			return os.MkdirAll(filepath.Join(dest, relative), 0o700)
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, relative)
		if entry.Type()&fs.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(linkTarget, target)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("dev source entry %s is not a regular file or symlink", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
}

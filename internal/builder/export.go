package builder

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ingot-agent/ingot/internal/image"
)

const ExportBuildManifestName = "ingot-build-manifest.json"

type ExportOptions struct {
	OutputDirectory string
	GOMODCACHE      string
}

type ExportResult struct {
	SourceDirectory string       `json:"source_directory"`
	BuildManifest   string       `json:"build_manifest"`
	ExpectedImageID string       `json:"expected_image_id"`
	Target          image.Target `json:"target"`
}

func (options ExportOptions) defaults() (ExportOptions, error) {
	if options.OutputDirectory == "" {
		return options, &Error{Code: "INGOT-GENERATE-OUTPUT", Want: "a non-empty output directory"}
	}
	absolute, err := cleanAbsolutePath(options.OutputDirectory)
	if err != nil {
		return options, &Error{Code: "INGOT-GENERATE-OUTPUT", Path: options.OutputDirectory, Err: err}
	}
	options.OutputDirectory = absolute
	return options, nil
}

// ExportRuntimeSource validates and generates the locked runtime as a
// standalone Go module without compiling a binary, running the runtime check,
// or creating an Image.
func ExportRuntimeSource(ctx context.Context, desired *DesiredPlugins, lock *Lock, options ExportOptions) (*ExportResult, error) {
	if err := validateGeneratedRuntimeInputs(desired, lock); err != nil {
		return nil, err
	}
	options, err := options.defaults()
	if err != nil {
		return nil, err
	}
	if err := validateExportOverlap(options.OutputDirectory, lock); err != nil {
		return nil, err
	}
	if _, _, err := inspectExportDestination(options.OutputDirectory); err != nil {
		return nil, err
	}
	parent := filepath.Dir(options.OutputDirectory)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, &Error{Code: "INGOT-GENERATE-OUTPUT", Path: parent, Err: err}
	}
	staging, err := os.MkdirTemp(parent, "."+filepath.Base(options.OutputDirectory)+".staging-")
	if err != nil {
		return nil, &Error{Code: "INGOT-GENERATE-OUTPUT", Path: parent, Err: err}
	}
	defer func() { _ = os.RemoveAll(staging) }()

	if _, err := prepareGeneratedRuntime(ctx, lock, staging, staging, options.GOMODCACHE); err != nil {
		return nil, err
	}
	buildManifest, err := lock.CanonicalBuildManifest()
	if err != nil {
		return nil, err
	}
	manifestPath := filepath.Join(staging, ExportBuildManifestName)
	if err := os.WriteFile(manifestPath, buildManifest, 0o644); err != nil {
		return nil, err
	}
	imageID, err := lock.ImageID()
	if err != nil {
		return nil, err
	}
	target, err := image.TargetFromBuildManifest(buildManifest)
	if err != nil {
		return nil, err
	}
	if err := syncTree(staging); err != nil {
		return nil, err
	}
	if err := publishExportDirectory(staging, options.OutputDirectory); err != nil {
		return nil, err
	}
	return &ExportResult{
		SourceDirectory: options.OutputDirectory,
		BuildManifest:   filepath.Join(options.OutputDirectory, ExportBuildManifestName),
		ExpectedImageID: imageID,
		Target:          target,
	}, nil
}

func validateExportOverlap(output string, lock *Lock) error {
	resolvedOutput, err := canonicalPath(output)
	if err != nil {
		return &Error{Code: "INGOT-GENERATE-OUTPUT", Path: output, Err: err}
	}
	for _, replacement := range lock.Replacements {
		resolvedSource, err := canonicalPath(replacement.DevPath)
		if err != nil {
			return &Error{Code: "INGOT-GENERATE-OUTPUT", Path: replacement.DevPath, Err: err}
		}
		inside, err := pathWithin(resolvedSource, resolvedOutput)
		if err != nil {
			return err
		}
		if inside {
			return &Error{Code: "INGOT-GENERATE-OUTPUT-OVERLAP", Path: output, Plugin: replacement.ModulePath, Want: "a directory outside local replacement sources"}
		}
	}
	return nil
}

// canonicalPath resolves existing symlink ancestors while retaining missing
// path components, so overlap checks are reliable before the destination is
// created.
func canonicalPath(path string) (string, error) {
	absolute, err := cleanAbsolutePath(path)
	if err != nil {
		return "", err
	}
	current := absolute
	missing := []string{}
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func pathWithin(root, candidate string) (bool, error) {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false, err
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))), nil
}

func inspectExportDestination(path string) (bool, fs.FileMode, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, &Error{Code: "INGOT-GENERATE-OUTPUT", Path: path, Err: err}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, 0, &Error{Code: "INGOT-GENERATE-OUTPUT-EXISTS", Path: path, Want: "a missing or empty directory"}
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, 0, &Error{Code: "INGOT-GENERATE-OUTPUT", Path: path, Err: err}
	}
	if len(entries) != 0 {
		return false, 0, &Error{Code: "INGOT-GENERATE-OUTPUT-EXISTS", Path: path, Want: "a missing or empty directory"}
	}
	return true, info.Mode().Perm(), nil
}

func publishExportDirectory(staging, destination string) error {
	existed, mode, err := inspectExportDestination(destination)
	if err != nil {
		return err
	}
	if existed {
		if err := os.Remove(destination); err != nil {
			return &Error{Code: "INGOT-GENERATE-PUBLISH", Path: destination, Err: err}
		}
	}
	if err := os.Rename(staging, destination); err != nil {
		if existed {
			_ = os.Mkdir(destination, mode)
		}
		return &Error{Code: "INGOT-GENERATE-PUBLISH", Path: destination, Err: err}
	}
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		return &Error{Code: "INGOT-GENERATE-PUBLISH", Path: destination, Err: err}
	}
	return nil
}

func syncTree(root string) error {
	directories := []string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			directories = append(directories, path)
			return nil
		}
		if entry.Type().IsRegular() {
			return syncFile(path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for index := len(directories) - 1; index >= 0; index-- {
		if err := syncDirectory(directories[index]); err != nil {
			return err
		}
	}
	return nil
}

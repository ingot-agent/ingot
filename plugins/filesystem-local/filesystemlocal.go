// Package filesystemlocal provides a workspace-scoped local filesystem.
package filesystemlocal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/ingot-agent/sdk"
	"github.com/ingot-agent/sdk/filesystem"
)

var (
	// ErrInvalidConfig indicates that the workspace root configuration is invalid.
	ErrInvalidConfig = errors.New("invalid filesystem.local config")
	// ErrInvalidPath indicates that a logical workspace path is malformed.
	ErrInvalidPath = errors.New("invalid workspace path")
	// ErrPathEscape indicates that a host path escaped the canonical workspace root.
	ErrPathEscape = errors.New("workspace path escape")
	// ErrSymlinkUnsupported indicates that an operation path traversed a symlink.
	ErrSymlinkUnsupported = errors.New("workspace symlink unsupported")
	// ErrRootChanged indicates that the configured workspace root no longer has
	// the identity captured when the component was constructed.
	ErrRootChanged = errors.New("workspace root changed")
)

// Config configures the workspace root exposed by the component.
type Config struct {
	Root string `toml:"root"`
}

// Dependencies contains the component's consumed capabilities.
type Dependencies struct{}

// Exports contains the component's provided capabilities.
type Exports struct {
	FS filesystem.FS
}

// New resolves and validates the workspace root.
func New(
	ctx context.Context,
	cfg Config,
	_ Dependencies,
) (Exports, sdk.Cleanup, error) {
	if ctx == nil || cfg.Root == "" {
		return Exports{}, nil, fmt.Errorf("root must be non-empty: %w", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}

	absolute, err := filepath.Abs(cfg.Root)
	if err != nil {
		return Exports{}, nil, fmt.Errorf("resolve workspace root %q: %w: %w", cfg.Root, ErrInvalidConfig, err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return Exports{}, nil, fmt.Errorf("resolve workspace root %q: %w: %w", cfg.Root, ErrInvalidConfig, err)
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return Exports{}, nil, fmt.Errorf("canonical workspace root %q: %w: %w", cfg.Root, ErrInvalidConfig, err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return Exports{}, nil, fmt.Errorf("stat workspace root %q: %w: %w", cfg.Root, ErrInvalidConfig, err)
	}
	if !info.IsDir() {
		return Exports{}, nil, fmt.Errorf("workspace root %q is not a directory: %w", cfg.Root, ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}

	canonical = filepath.Clean(canonical)
	rootHandle, err := os.Open(canonical)
	if err != nil {
		return Exports{}, nil, fmt.Errorf("open workspace root %q: %w: %w", cfg.Root, ErrInvalidConfig, err)
	}
	rootInfo, err := rootHandle.Stat()
	if err != nil {
		_ = rootHandle.Close()
		return Exports{}, nil, fmt.Errorf("stat workspace root handle %q: %w: %w", cfg.Root, ErrInvalidConfig, err)
	}
	created := &localFS{root: canonical, rootHandle: rootHandle, rootInfo: rootInfo}
	cleanup := sdk.Cleanup(func(ctx context.Context) error {
		created.closeOnce.Do(func() { created.closeErr = rootHandle.Close() })
		if created.closeErr != nil {
			return created.closeErr
		}
		if ctx == nil {
			return context.Canceled
		}
		return ctx.Err()
	})
	return Exports{FS: created}, cleanup, nil
}

type localFS struct {
	root       string
	rootHandle *os.File
	rootInfo   fs.FileInfo
	closeOnce  sync.Once
	closeErr   error
}

func (f *localFS) ensureRoot() error {
	if f.rootHandle == nil || f.rootInfo == nil {
		return fmt.Errorf("workspace root handle is unavailable: %w", ErrRootChanged)
	}
	handleInfo, err := f.rootHandle.Stat()
	if err != nil {
		return fmt.Errorf("stat workspace root handle: %w: %w", ErrRootChanged, err)
	}
	pathInfo, err := os.Stat(f.root)
	if err != nil {
		return fmt.Errorf("stat workspace root path: %w: %w", ErrRootChanged, err)
	}
	if !handleInfo.IsDir() || !pathInfo.IsDir() || !os.SameFile(f.rootInfo, handleInfo) || !os.SameFile(f.rootInfo, pathInfo) {
		return fmt.Errorf("workspace root identity changed: %w", ErrRootChanged)
	}
	return nil
}

func (f *localFS) ReadFile(ctx context.Context, logical string) ([]byte, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if err := f.ensureRoot(); err != nil {
		return nil, operationError("read", logical, err)
	}
	host, info, err := f.resolveExisting(logical)
	if err != nil {
		return nil, operationError("read", logical, err)
	}
	if !info.Mode().IsRegular() {
		return nil, operationError("read", logical, fmt.Errorf("not a regular file: %w", fs.ErrInvalid))
	}
	if err := f.ensureRoot(); err != nil {
		return nil, operationError("read", logical, err)
	}

	file, err := os.Open(host)
	if err != nil {
		return nil, operationError("read", logical, err)
	}
	defer file.Close()

	var output bytes.Buffer
	buffer := make([]byte, 64*1024)
	for {
		if err := checkContext(ctx); err != nil {
			return nil, err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			_, _ = output.Write(buffer[:count])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, operationError("read", logical, readErr)
		}
	}
	if err := f.ensureRoot(); err != nil {
		return nil, operationError("read", logical, err)
	}
	return output.Bytes(), nil
}

func (f *localFS) WriteFile(ctx context.Context, logical string, data []byte, mode fs.FileMode) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := f.ensureRoot(); err != nil {
		return operationError("write", logical, err)
	}
	if mode&^fs.ModePerm != 0 {
		return operationError("write", logical, fmt.Errorf("mode %v contains non-permission bits: %w", mode, fs.ErrInvalid))
	}
	if err := validateLogicalPath(logical); err != nil {
		return operationError("write", logical, err)
	}

	parentLogical := path.Dir(logical)
	if parentLogical == "." && logical == "." {
		return operationError("write", logical, fmt.Errorf("workspace root is not a file: %w", fs.ErrInvalid))
	}
	parentHost, parentInfo, err := f.resolveExisting(parentLogical)
	if err != nil {
		return operationError("write", logical, err)
	}
	if !parentInfo.IsDir() {
		return operationError("write", logical, fmt.Errorf("parent is not a directory: %w", fs.ErrInvalid))
	}
	targetHost, err := f.joinValidated(logical)
	if err != nil {
		return operationError("write", logical, err)
	}
	if targetInfo, statErr := os.Lstat(targetHost); statErr == nil {
		if err := rejectSymlink(targetInfo); err != nil {
			return operationError("write", logical, err)
		}
		if !targetInfo.Mode().IsRegular() {
			return operationError("write", logical, fmt.Errorf("target is not a regular file: %w", fs.ErrInvalid))
		}
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return operationError("write", logical, statErr)
	}

	temporary, err := os.CreateTemp(parentHost, ".ingot-write-*")
	if err != nil {
		return operationError("write", logical, err)
	}
	temporaryName := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryName)
		}
	}()

	if err := temporary.Chmod(mode.Perm()); err != nil {
		return operationError("write", logical, err)
	}
	for offset := 0; offset < len(data); {
		if err := checkContext(ctx); err != nil {
			return err
		}
		end := min(offset+64*1024, len(data))
		count, writeErr := temporary.Write(data[offset:end])
		offset += count
		if writeErr != nil {
			return operationError("write", logical, writeErr)
		}
		if count == 0 {
			return operationError("write", logical, io.ErrShortWrite)
		}
	}
	if err := temporary.Close(); err != nil {
		return operationError("write", logical, err)
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := f.ensureRoot(); err != nil {
		return operationError("write", logical, err)
	}
	if err := atomicReplace(temporaryName, targetHost); err != nil {
		return operationError("write", logical, err)
	}
	committed = true
	if err := f.ensureRoot(); err != nil {
		return operationError("write", logical, err)
	}
	return nil
}

func (f *localFS) ReadDir(ctx context.Context, logical string) ([]fs.DirEntry, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if err := f.ensureRoot(); err != nil {
		return nil, operationError("read directory", logical, err)
	}
	host, info, err := f.resolveExisting(logical)
	if err != nil {
		return nil, operationError("read directory", logical, err)
	}
	if !info.IsDir() {
		return nil, operationError("read directory", logical, fmt.Errorf("not a directory: %w", fs.ErrInvalid))
	}
	if err := f.ensureRoot(); err != nil {
		return nil, operationError("read directory", logical, err)
	}
	entries, err := os.ReadDir(host)
	if err != nil {
		return nil, operationError("read directory", logical, err)
	}
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if err := f.ensureRoot(); err != nil {
		return nil, operationError("read directory", logical, err)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	result := make([]fs.DirEntry, len(entries))
	copy(result, entries)
	return result, nil
}

func (f *localFS) Stat(ctx context.Context, logical string) (fs.FileInfo, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if err := f.ensureRoot(); err != nil {
		return nil, operationError("stat", logical, err)
	}
	_, info, err := f.resolveExisting(logical)
	if err != nil {
		return nil, operationError("stat", logical, err)
	}
	if err := f.ensureRoot(); err != nil {
		return nil, operationError("stat", logical, err)
	}
	return info, nil
}

func (f *localFS) MkdirAll(ctx context.Context, logical string, mode fs.FileMode) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := f.ensureRoot(); err != nil {
		return operationError("mkdir", logical, err)
	}
	if mode&^fs.ModePerm != 0 {
		return operationError("mkdir", logical, fmt.Errorf("mode %v contains non-permission bits: %w", mode, fs.ErrInvalid))
	}
	segments, err := logicalSegments(logical)
	if err != nil {
		return operationError("mkdir", logical, err)
	}
	current := f.root
	for _, segment := range segments {
		if err := checkContext(ctx); err != nil {
			return err
		}
		if err := f.ensureRoot(); err != nil {
			return operationError("mkdir", logical, err)
		}
		current = filepath.Join(current, segment)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, fs.ErrNotExist) {
			if mkdirErr := os.Mkdir(current, mode.Perm()); mkdirErr != nil && !errors.Is(mkdirErr, fs.ErrExist) {
				return operationError("mkdir", logical, mkdirErr)
			}
			info, statErr = os.Lstat(current)
		}
		if statErr != nil {
			return operationError("mkdir", logical, statErr)
		}
		if err := rejectSymlink(info); err != nil {
			return operationError("mkdir", logical, err)
		}
		if !info.IsDir() {
			return operationError("mkdir", logical, fmt.Errorf("path component is not a directory: %w", fs.ErrExist))
		}
	}
	if err := f.ensureRoot(); err != nil {
		return operationError("mkdir", logical, err)
	}
	return nil
}

func (f *localFS) Remove(ctx context.Context, logical string) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := f.ensureRoot(); err != nil {
		return operationError("remove", logical, err)
	}
	host, info, err := f.resolveExisting(logical)
	if err != nil {
		return operationError("remove", logical, err)
	}
	if logical == "." {
		return operationError("remove", logical, fmt.Errorf("cannot remove workspace root: %w", fs.ErrPermission))
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return operationError("remove", logical, fmt.Errorf("unsupported file type: %w", fs.ErrInvalid))
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := f.ensureRoot(); err != nil {
		return operationError("remove", logical, err)
	}
	if err := os.Remove(host); err != nil {
		return operationError("remove", logical, err)
	}
	if err := f.ensureRoot(); err != nil {
		return operationError("remove", logical, err)
	}
	return nil
}

func (f *localFS) Rename(ctx context.Context, source, destination string) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := f.ensureRoot(); err != nil {
		return operationError("rename source", source, err)
	}
	sourceHost, _, err := f.resolveExisting(source)
	if err != nil {
		return operationError("rename source", source, err)
	}
	if source == "." {
		return operationError("rename source", source, fmt.Errorf("cannot rename workspace root: %w", fs.ErrPermission))
	}
	if err := validateLogicalPath(destination); err != nil {
		return operationError("rename destination", destination, err)
	}
	destinationParent := path.Dir(destination)
	parentHost, parentInfo, err := f.resolveExisting(destinationParent)
	if err != nil {
		return operationError("rename destination", destination, err)
	}
	if !parentInfo.IsDir() {
		return operationError("rename destination", destination, fmt.Errorf("parent is not a directory: %w", fs.ErrInvalid))
	}
	destinationHost := filepath.Join(parentHost, filepath.Base(filepath.FromSlash(destination)))
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := f.ensureRoot(); err != nil {
		return operationError("rename", source+" -> "+destination, err)
	}
	if err := atomicRenameNoReplace(sourceHost, destinationHost); err != nil {
		return operationError("rename", source+" -> "+destination, err)
	}
	if err := f.ensureRoot(); err != nil {
		return operationError("rename", source+" -> "+destination, err)
	}
	return nil
}

func (f *localFS) resolveExisting(logical string) (string, fs.FileInfo, error) {
	if err := f.ensureRoot(); err != nil {
		return "", nil, err
	}
	segments, err := logicalSegments(logical)
	if err != nil {
		return "", nil, err
	}
	current := f.root
	var info fs.FileInfo
	if len(segments) == 0 {
		info, err = os.Lstat(current)
		return current, info, err
	}
	for _, segment := range segments {
		current = filepath.Join(current, segment)
		info, err = os.Lstat(current)
		if err != nil {
			return "", nil, err
		}
		if err := rejectSymlink(info); err != nil {
			return "", nil, err
		}
	}
	if err := f.ensureBoundary(current); err != nil {
		return "", nil, err
	}
	if err := f.ensureRoot(); err != nil {
		return "", nil, err
	}
	return current, info, nil
}

func (f *localFS) joinValidated(logical string) (string, error) {
	if err := f.ensureRoot(); err != nil {
		return "", err
	}
	segments, err := logicalSegments(logical)
	if err != nil {
		return "", err
	}
	joined := filepath.Join(append([]string{f.root}, segments...)...)
	if err := f.ensureBoundary(joined); err != nil {
		return "", err
	}
	if err := f.ensureRoot(); err != nil {
		return "", err
	}
	return joined, nil
}

func (f *localFS) ensureBoundary(host string) error {
	relative, err := filepath.Rel(f.root, host)
	if err != nil {
		return fmt.Errorf("compare workspace boundary: %w: %w", ErrPathEscape, err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return fmt.Errorf("%w: %w", ErrPathEscape, fs.ErrPermission)
	}
	return nil
}

func logicalSegments(logical string) ([]string, error) {
	if err := validateLogicalPath(logical); err != nil {
		return nil, err
	}
	if logical == "." {
		return nil, nil
	}
	return strings.Split(logical, "/"), nil
}

func validateLogicalPath(logical string) error {
	if !utf8.ValidString(logical) || strings.ContainsRune(logical, '\x00') || strings.Contains(logical, `\`) || !fs.ValidPath(logical) {
		return fmt.Errorf("%w: %w", ErrInvalidPath, fs.ErrInvalid)
	}
	if runtime.GOOS == "windows" && strings.Contains(logical, ":") {
		return fmt.Errorf("%w: %w", ErrInvalidPath, fs.ErrInvalid)
	}
	return nil
}

func rejectSymlink(info fs.FileInfo) error {
	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%w: %w", ErrSymlinkUnsupported, fs.ErrPermission)
	}
	return nil
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	return ctx.Err()
}

func operationError(operation, logical string, err error) error {
	return fmt.Errorf("%s %q: %w", operation, logical, err)
}

var _ filesystem.FS = (*localFS)(nil)

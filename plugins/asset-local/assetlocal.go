// Package assetlocal implements persistent immutable assets in plugin-scoped
// local state.
package assetlocal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sync"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/asset"
)

const (
	defaultMaxObjectBytes = 64 * 1024 * 1024
	defaultMaxTotalBytes  = 10 * 1024 * 1024 * 1024
	defaultIOConcurrency  = 8
	referencePrefix       = "sha256:"
)

var (
	// ErrInvalidConfig indicates invalid limits or host state dependency.
	ErrInvalidConfig = errors.New("invalid asset.local config")
	// ErrInvalidReference indicates a zero or malformed local reference.
	ErrInvalidReference = errors.New("invalid asset reference")
	// ErrNotFound indicates that this store cannot resolve a reference.
	ErrNotFound = errors.New("asset not found")
	// ErrObjectLimit indicates that one declared asset exceeds the configured
	// per-object bound.
	ErrObjectLimit = errors.New("asset object limit exceeded")
	// ErrCapacity indicates that committing an asset would exceed total local
	// capacity.
	ErrCapacity      = errors.New("asset store capacity exceeded")
	referencePattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// Config bounds immutable local storage and concurrent I/O.
type Config struct {
	MaxObjectBytes int64 `toml:"max_object_bytes"`
	MaxTotalBytes  int64 `toml:"max_total_bytes"`
	IOConcurrency  int   `toml:"io_concurrency"`
}

// Dependencies contains the plugin-scoped persistent state location.
type Dependencies struct {
	State state.Scope
}

// Exports contains one store value. Because asset.Store embeds
// asset.Resolver, the same export satisfies both capability targets without
// creating two ambiguous providers in an ingot component graph.
type Exports struct {
	Store asset.Store
}

type store struct {
	root      string
	blobs     string
	staging   string
	maxObject uint64
	maxTotal  uint64
	slots     chan struct{}

	mu    sync.Mutex
	total uint64
}

// New validates state, removes incomplete staging files, and indexes durable
// blobs without reading their contents.
func New(ctx context.Context, cfg Config, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil || isNil(deps.State) {
		return Exports{}, nil, fmt.Errorf("construct asset.local: %w", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	maxObject, err := positiveDefault(cfg.MaxObjectBytes, defaultMaxObjectBytes, "max_object_bytes")
	if err != nil {
		return Exports{}, nil, err
	}
	maxTotal, err := positiveDefault(cfg.MaxTotalBytes, defaultMaxTotalBytes, "max_total_bytes")
	if err != nil {
		return Exports{}, nil, err
	}
	if maxObject > maxTotal {
		return Exports{}, nil, fmt.Errorf("max_object_bytes exceeds max_total_bytes: %w", ErrInvalidConfig)
	}
	concurrency := cfg.IOConcurrency
	if concurrency == 0 {
		concurrency = defaultIOConcurrency
	}
	if concurrency < 1 {
		return Exports{}, nil, fmt.Errorf("io_concurrency must be positive: %w", ErrInvalidConfig)
	}
	root := deps.State.Dir()
	if root == "" || !filepath.IsAbs(root) {
		return Exports{}, nil, fmt.Errorf("asset state directory must be absolute and non-empty: %w", ErrInvalidConfig)
	}
	root = filepath.Clean(root)
	instance := &store{
		root: root, blobs: filepath.Join(root, "blobs"), staging: filepath.Join(root, "staging"),
		maxObject: uint64(maxObject), maxTotal: uint64(maxTotal), slots: make(chan struct{}, concurrency),
	}
	if err := instance.initialize(ctx); err != nil {
		return Exports{}, nil, err
	}
	return Exports{Store: instance}, nil, nil
}

func positiveDefault(value, fallback int64, field string) (int64, error) {
	if value == 0 {
		return fallback, nil
	}
	if value < 1 {
		return 0, fmt.Errorf("%s must be positive: %w", field, ErrInvalidConfig)
	}
	return value, nil
}

func (s *store) initialize(ctx context.Context) error {
	if err := os.MkdirAll(s.blobs, 0o700); err != nil {
		return fmt.Errorf("create asset blob directory: %w", err)
	}
	if err := os.MkdirAll(s.staging, 0o700); err != nil {
		return fmt.Errorf("create asset staging directory: %w", err)
	}
	staged, err := os.ReadDir(s.staging)
	if err != nil {
		return fmt.Errorf("inspect asset staging directory: %w", err)
	}
	for _, entry := range staged {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(s.staging, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove incomplete asset staging entry: %w", err)
		}
	}
	var total uint64
	err = filepath.WalkDir(s.blobs, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("asset blob %q is not a regular file", path)
		}
		if uint64(info.Size()) > ^uint64(0)-total {
			return errors.New("asset store size overflow")
		}
		total += uint64(info.Size())
		return nil
	})
	if err != nil {
		return fmt.Errorf("index asset blobs: %w", err)
	}
	if total > s.maxTotal {
		return fmt.Errorf("stored assets exceed configured capacity: %w", ErrCapacity)
	}
	s.total = total
	return nil
}

func (s *store) Put(ctx context.Context, request asset.PutRequest) (asset.Reference, asset.Info, error) {
	if ctx == nil || request.Body == nil {
		return asset.Reference{}, asset.Info{}, errors.New("put asset: nil context or body")
	}
	if err := ctx.Err(); err != nil {
		return asset.Reference{}, asset.Info{}, err
	}
	if request.Size > s.maxObject {
		return asset.Reference{}, asset.Info{}, fmt.Errorf("declared size %d exceeds %d: %w", request.Size, s.maxObject, ErrObjectLimit)
	}
	if err := s.acquire(ctx); err != nil {
		return asset.Reference{}, asset.Info{}, err
	}
	defer s.release()

	temporary, err := os.CreateTemp(s.staging, ".asset-*")
	if err != nil {
		return asset.Reference{}, asset.Info{}, fmt.Errorf("create asset staging file: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return asset.Reference{}, asset.Info{}, fmt.Errorf("secure asset staging file: %w", err)
	}
	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(temporary, digest), io.LimitReader(&contextReader{ctx: ctx, reader: request.Body}, int64(request.Size)+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return asset.Reference{}, asset.Info{}, ctxErr
		}
		return asset.Reference{}, asset.Info{}, fmt.Errorf("read asset body: %w", err)
	}
	if uint64(written) != request.Size {
		return asset.Reference{}, asset.Info{}, fmt.Errorf("asset body size %d does not match declared size %d", written, request.Size)
	}
	if err := temporary.Sync(); err != nil {
		return asset.Reference{}, asset.Info{}, fmt.Errorf("sync asset staging file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return asset.Reference{}, asset.Info{}, fmt.Errorf("close asset staging file: %w", err)
	}
	reference := asset.Reference{ID: referencePrefix + hex.EncodeToString(digest.Sum(nil))}
	destination := s.path(reference)
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return asset.Reference{}, asset.Info{}, fmt.Errorf("create asset shard directory: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, err := os.Stat(destination); err == nil {
		if !existing.Mode().IsRegular() || uint64(existing.Size()) != request.Size {
			return asset.Reference{}, asset.Info{}, errors.New("asset digest collision or corrupt stored blob")
		}
		if err := verifyBlob(destination, reference); err != nil {
			return asset.Reference{}, asset.Info{}, err
		}
		return reference, asset.Info{Size: request.Size}, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return asset.Reference{}, asset.Info{}, fmt.Errorf("inspect asset destination: %w", err)
	}
	if request.Size > s.maxTotal-s.total {
		return asset.Reference{}, asset.Info{}, ErrCapacity
	}
	if err := os.Link(temporaryPath, destination); err != nil {
		if existing, statErr := os.Stat(destination); statErr == nil && existing.Mode().IsRegular() && uint64(existing.Size()) == request.Size {
			if verifyErr := verifyBlob(destination, reference); verifyErr != nil {
				return asset.Reference{}, asset.Info{}, verifyErr
			}
			return reference, asset.Info{Size: request.Size}, nil
		}
		return asset.Reference{}, asset.Info{}, fmt.Errorf("publish asset: %w", err)
	}
	committed = true
	s.total += request.Size
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		return asset.Reference{}, asset.Info{}, fmt.Errorf("sync asset shard directory: %w", err)
	}
	_ = os.Remove(temporaryPath)
	return reference, asset.Info{Size: request.Size}, nil
}

func verifyBlob(path string, reference asset.Reference) error {
	body, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("verify stored asset: %w", err)
	}
	digest := sha256.New()
	_, readErr := io.Copy(digest, body)
	closeErr := body.Close()
	if readErr != nil {
		return fmt.Errorf("verify stored asset: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close verified asset: %w", closeErr)
	}
	actual := referencePrefix + hex.EncodeToString(digest.Sum(nil))
	if actual != reference.ID {
		return errors.New("asset digest collision or corrupt stored blob")
	}
	return nil
}

func (s *store) Stat(ctx context.Context, reference asset.Reference) (asset.Info, error) {
	if ctx == nil {
		return asset.Info{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return asset.Info{}, err
	}
	path, err := s.referencePath(reference)
	if err != nil {
		return asset.Info{}, err
	}
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return asset.Info{}, fmt.Errorf("reference cannot be resolved: %w: %w", ErrNotFound, fs.ErrNotExist)
	}
	if err != nil {
		return asset.Info{}, fmt.Errorf("stat asset: %w", err)
	}
	if !info.Mode().IsRegular() {
		return asset.Info{}, errors.New("asset path is not a regular file")
	}
	return asset.Info{Size: uint64(info.Size())}, nil
}

func (s *store) Open(ctx context.Context, reference asset.Reference) (io.ReadCloser, error) {
	if ctx == nil {
		return nil, context.Canceled
	}
	path, err := s.referencePath(reference)
	if err != nil {
		return nil, err
	}
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		s.release()
		return nil, fmt.Errorf("reference cannot be resolved: %w: %w", ErrNotFound, fs.ErrNotExist)
	}
	if err != nil {
		s.release()
		return nil, fmt.Errorf("open asset: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		s.release()
		return nil, errors.New("asset path is not a regular file")
	}
	return &slotReader{ReadCloser: file, release: s.release}, nil
}

func (s *store) referencePath(reference asset.Reference) (string, error) {
	if !referencePattern.MatchString(reference.ID) {
		return "", ErrInvalidReference
	}
	return s.path(reference), nil
}

func (s *store) path(reference asset.Reference) string {
	digest := reference.ID[len(referencePrefix):]
	return filepath.Join(s.blobs, digest[:2], digest)
}

func (s *store) acquire(ctx context.Context) error {
	select {
	case s.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *store) release() { <-s.slots }

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

type slotReader struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

func (r *slotReader) Close() error {
	err := r.ReadCloser.Close()
	r.once.Do(r.release)
	return err
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var (
	_ asset.Store    = (*store)(nil)
	_ asset.Resolver = (*store)(nil)
)

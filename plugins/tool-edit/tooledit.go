// Package tooledit exposes workspace file tools for editing, reading, and
// searching UTF-8 text in the session workspace: edit_file, read_file, and
// search. All tools resolve paths only through the session Workspace binding.
package tooledit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/operation"
	"github.com/ingot-agent/sdk/tool"
	"github.com/ingot-agent/sdk/workspace"
)

const (
	// toolName is the stable public name of the edit tool.
	toolName = "edit_file"
	// readToolName is the stable public name of the read tool.
	readToolName = "read_file"
	// searchToolName is the stable public name of the search tool.
	searchToolName = "search"
	// defaultMaxFileBytes bounds a single edited/read/search file's in-memory size.
	defaultMaxFileBytes = 1 << 20 // 1 MiB
	// defaultMaxScanBytes bounds the total bytes searched for recursive search.
	defaultMaxScanBytes = 4 << 20 // 4 MiB
)

var (
	// ErrInvalidConfig indicates invalid tool.edit configuration.
	ErrInvalidConfig = errors.New("invalid tool.edit config")
	// ErrInvalidArguments indicates malformed edit_file arguments.
	ErrInvalidArguments = errors.New("invalid tool.edit arguments")
	// ErrUnsafePath indicates that a path argument escapes the workspace root.
	ErrUnsafePath = errors.New("tool.edit path escapes workspace root")
)

// Config bounds file sizes handled by the tools.
type Config struct {
	MaxFileBytes int `toml:"max_file_bytes"`
	MaxScanBytes int `toml:"max_scan_bytes"`
}

// Dependencies resolves the session workspace that owns edited files, plus
// this Plugin's own persistent state scope.
type Dependencies struct {
	Workspace workspace.Resolver
	State     state.Scope
}

// Exports contains the workspace file tools plus this Plugin's own
// configuration Operation. Exporting it through the ordinary capability graph
// keeps the Host free of any plugin-specific knowledge.
type Exports struct {
	Tools      []tool.Tool
	Operations []operation.Operation
}

type normalizedConfig struct {
	maxFileBytes int
	maxScanBytes int
}

type editTool struct {
	config    normalizedConfig
	workspace workspace.Resolver
}

type editArguments struct {
	Path       *string `json:"path"`
	Old        *string `json:"old"`
	New        string  `json:"new"`
	ReplaceAll bool    `json:"replace_all"`
}

// New validates dependencies, loads this Plugin's own configuration from its
// Runtime state scope, and creates the tool set. A missing configuration file
// is the normal Unconfigured state; defaults apply.
func New(ctx context.Context, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, fmt.Errorf("construct tool.edit: %w", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	if isNil(deps.Workspace) || isNil(deps.State) {
		return Exports{}, nil, fmt.Errorf("workspace and state dependencies are required: %w", ErrInvalidConfig)
	}
	cfg, err := loadConfig(deps.State.Dir())
	if err != nil {
		return Exports{}, nil, fmt.Errorf("construct tool.edit: %w: %w", err, ErrInvalidConfig)
	}
	config, err := normalizeConfig(cfg)
	if err != nil {
		return Exports{}, nil, err
	}
	return Exports{
		Tools: []tool.Tool{
			&editTool{config: config, workspace: deps.Workspace},
			&readTool{config: config, workspace: deps.Workspace},
			&searchTool{config: config, workspace: deps.Workspace},
		},
		Operations: []operation.Operation{&setupOperation{scope: deps.State}},
	}, nil, nil
}

func (t *editTool) Definition() tool.Definition {
	return tool.Definition{
		Name: toolName,
		Description: "Replace an exact UTF-8 text substring in a workspace-relative file. " +
			"By default only the first occurrence is replaced; set replace_all to true to " +
			"replace every occurrence. new defaults to an empty string (deletion). " +
			"Example: {\"path\":\"src/main.go\",\"old\":\"foo\",\"new\":\"bar\",\"replace_all\":true}",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["path","old"],"properties":{"path":{"type":"string","minLength":1},"old":{"type":"string","minLength":1},"new":{"type":"string"},"replace_all":{"type":"boolean"}}}`),
	}
}

// businessResult reports a known edit_file failure as a normal tool result so
// the model can read the reason and continue. A canceled or expired parent
// context is preserved as an error so the surrounding turn stops.
func (t *editTool) businessResult(ctx context.Context, message string) (tool.Result, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return tool.Result{}, ctxErr
	}
	return tool.Result{Content: content.FromText(message)}, nil
}

func (t *editTool) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	if ctx == nil {
		return tool.Result{}, fmt.Errorf("edit_file: nil context")
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	call := invocation.Call
	if call.Name != "" && call.Name != toolName {
		return tool.Result{}, fmt.Errorf("call name %q: %w", call.Name, ErrInvalidArguments)
	}
	binding, err := t.workspace.Resolve(ctx, invocation.Scope)
	if err != nil {
		return tool.Result{}, fmt.Errorf("edit_file resolve workspace for session %q: %w", invocation.Scope.SessionID, err)
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	var args editArguments
	if err := decodeObject(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if args.Path == nil || *args.Path == "" || !utf8.ValidString(*args.Path) {
		return tool.Result{}, fmt.Errorf("path must be a non-empty UTF-8 string: %w", ErrInvalidArguments)
	}
	if args.Old == nil || *args.Old == "" || !utf8.ValidString(*args.Old) {
		return tool.Result{}, fmt.Errorf("old must be a non-empty UTF-8 string: %w", ErrInvalidArguments)
	}
	if !utf8.ValidString(args.New) {
		return tool.Result{}, fmt.Errorf("new must be valid UTF-8: %w", ErrInvalidArguments)
	}
	target, err := resolveTarget(binding.Root, *args.Path)
	if err != nil {
		// Path-policy rejections (absolute path, ".." traversal) are business
		// outcomes: the model must read the reason and retry with a legal
		// workspace-relative path, so they are returned as a normal Result.
		// Real filesystem resolution failures (Abs/Rel) remain Go errors.
		if errors.Is(err, ErrUnsafePath) || errors.Is(err, ErrInvalidArguments) {
			return t.businessResult(ctx, fmt.Sprintf("edit_file error: %v", err))
		}
		return tool.Result{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return t.businessResult(ctx, fmt.Sprintf("edit_file error: file not found: %s", *args.Path))
		}
		return tool.Result{}, fmt.Errorf("edit_file stat %q: %w", *args.Path, err)
	}
	if info.IsDir() {
		return t.businessResult(ctx, fmt.Sprintf("edit_file error: path is a directory: %s", *args.Path))
	}
	if info.Size() > int64(t.config.maxFileBytes) {
		return t.businessResult(ctx, fmt.Sprintf("edit_file error: file exceeds %d bytes: %s", t.config.maxFileBytes, *args.Path))
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return tool.Result{}, fmt.Errorf("edit_file read %q: %w", *args.Path, err)
	}
	if !utf8.Valid(data) {
		return t.businessResult(ctx, fmt.Sprintf("edit_file error: file is not valid UTF-8: %s", *args.Path))
	}
	old, new := *args.Old, args.New
	if old == new {
		return t.businessResult(ctx, fmt.Sprintf("edit_file no-op: old text equals new text in %s", *args.Path))
	}
	count := strings.Count(string(data), old)
	if count == 0 {
		return t.businessResult(ctx, fmt.Sprintf("edit_file error: text not found in %s", *args.Path))
	}
	n := 1
	if args.ReplaceAll {
		n = -1
	}
	replaced := count
	if n == 1 {
		replaced = 1
	}
	updated := strings.Replace(string(data), old, new, n)
	if updated == string(data) {
		return t.businessResult(ctx, fmt.Sprintf("edit_file no-op: nothing changed in %s", *args.Path))
	}
	if err := writeFileAtomic(target, []byte(updated), info.Mode()); err != nil {
		return tool.Result{}, fmt.Errorf("edit_file write %q: %w", *args.Path, err)
	}
	return tool.Result{Content: content.FromText(fmt.Sprintf(
		"edited: %s\nreplacements: %d\nbytes: %d", *args.Path, replaced, len(updated)))}, nil
}

// resolveTarget joins a workspace-relative path to root and verifies the
// cleaned result stays within root. It rejects absolute paths and any ".."
// traversal. This is a path hygiene guard, not a filesystem security boundary:
// the workspace contract explicitly does not imply confinement.
func resolveTarget(root, relative string) (string, error) {
	if filepath.IsAbs(relative) {
		return "", fmt.Errorf("path must be workspace-relative: %w", ErrInvalidArguments)
	}
	for _, part := range strings.Split(filepath.ToSlash(relative), "/") {
		if part == ".." {
			return "", fmt.Errorf("path escapes workspace: %w", ErrUnsafePath)
		}
	}
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	joined, err := filepath.Abs(filepath.Join(cleanRoot, filepath.FromSlash(relative)))
	if err != nil {
		return "", fmt.Errorf("resolve target path: %w", err)
	}
	rel, err := filepath.Rel(cleanRoot, joined)
	if err != nil {
		return "", fmt.Errorf("resolve target path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes workspace: %w", ErrUnsafePath)
	}
	return joined, nil
}

// writeFileAtomic replaces a file's content atomically, preserving its
// original permission bits. A temporary file is created in the target
// directory so the rename stays on one filesystem and can never produce a
// partially written target.
func writeFileAtomic(target string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".tool-edit-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return err
	}
	committed = true
	return nil
}

func decodeObject(raw json.RawMessage, target any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return fmt.Errorf("arguments are required: %w", ErrInvalidArguments)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode arguments: %w: %w", ErrInvalidArguments, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("arguments contain trailing JSON: %w", ErrInvalidArguments)
		}
		return fmt.Errorf("decode trailing arguments: %w: %w", ErrInvalidArguments, err)
	}
	return nil
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}

var _ tool.Tool = (*editTool)(nil)

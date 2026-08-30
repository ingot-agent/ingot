// Package toolfs exposes a bounded filesystem tool set.
package toolfs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"reflect"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/filesystem"
	"github.com/ingot-agent/sdk/tool"
)

const (
	defaultMaxReadBytes   = 1024 * 1024
	defaultMaxListEntries = 1000
	defaultFileMode       = 0o644
	defaultDirectoryMode  = 0o755
	toolRead              = "fs_read"
	toolWrite             = "fs_write"
	toolList              = "fs_list"
	toolStat              = "fs_stat"
	toolMkdir             = "fs_mkdir"
	toolRemove            = "fs_remove"
	toolRename            = "fs_rename"
)

var (
	// ErrInvalidConfig indicates invalid tool.fs configuration.
	ErrInvalidConfig = errors.New("invalid tool.fs config")
	// ErrInvalidArguments indicates malformed filesystem tool arguments.
	ErrInvalidArguments = errors.New("invalid tool.fs arguments")
	// ErrBinaryContent indicates that a read result is not valid UTF-8 text.
	ErrBinaryContent = errors.New("binary content is not supported")
	// ErrResultLimit indicates that a configured result bound was exceeded.
	ErrResultLimit = errors.New("filesystem tool result limit exceeded")
)

// Config bounds filesystem tool operations.
type Config struct {
	MaxReadBytes   int `toml:"max_read_bytes"`
	MaxListEntries int `toml:"max_list_entries"`
	FileMode       int `toml:"file_mode"`
	DirectoryMode  int `toml:"directory_mode"`
}

// Dependencies contains the filesystem capability used by the tools.
type Dependencies struct {
	Filesystem filesystem.FS
}

// Exports contains the tools in their stable documented order.
type Exports struct {
	Tools []tool.Tool
}

type normalizedConfig struct {
	maxReadBytes   int
	maxListEntries int
	fileMode       fs.FileMode
	directoryMode  fs.FileMode
}

// New validates configuration and creates the seven filesystem tools.
func New(ctx context.Context, cfg Config, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, fmt.Errorf("construct tool.fs: %w", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	if isNil(deps.Filesystem) {
		return Exports{}, nil, fmt.Errorf("filesystem dependency is required: %w", ErrInvalidConfig)
	}
	normalized, err := normalizeConfig(cfg)
	if err != nil {
		return Exports{}, nil, err
	}
	tools := []tool.Tool{
		&fsTool{name: toolRead, filesystem: deps.Filesystem, config: normalized},
		&fsTool{name: toolWrite, filesystem: deps.Filesystem, config: normalized},
		&fsTool{name: toolList, filesystem: deps.Filesystem, config: normalized},
		&fsTool{name: toolStat, filesystem: deps.Filesystem, config: normalized},
		&fsTool{name: toolMkdir, filesystem: deps.Filesystem, config: normalized},
		&fsTool{name: toolRemove, filesystem: deps.Filesystem, config: normalized},
		&fsTool{name: toolRename, filesystem: deps.Filesystem, config: normalized},
	}
	return Exports{Tools: tools}, nil, nil
}

func normalizeConfig(cfg Config) (normalizedConfig, error) {
	maxRead := cfg.MaxReadBytes
	if maxRead == 0 {
		maxRead = defaultMaxReadBytes
	}
	if maxRead < 1 {
		return normalizedConfig{}, fmt.Errorf("max_read_bytes must be positive: %w", ErrInvalidConfig)
	}
	maxEntries := cfg.MaxListEntries
	if maxEntries == 0 {
		maxEntries = defaultMaxListEntries
	}
	if maxEntries < 1 {
		return normalizedConfig{}, fmt.Errorf("max_list_entries must be positive: %w", ErrInvalidConfig)
	}
	fileMode := cfg.FileMode
	if fileMode == 0 {
		fileMode = defaultFileMode
	}
	directoryMode := cfg.DirectoryMode
	if directoryMode == 0 {
		directoryMode = defaultDirectoryMode
	}
	if fileMode < 0 || fileMode&^int(fs.ModePerm) != 0 {
		return normalizedConfig{}, fmt.Errorf("file_mode must contain permission bits only: %w", ErrInvalidConfig)
	}
	if directoryMode < 0 || directoryMode&^int(fs.ModePerm) != 0 {
		return normalizedConfig{}, fmt.Errorf("directory_mode must contain permission bits only: %w", ErrInvalidConfig)
	}
	return normalizedConfig{maxReadBytes: maxRead, maxListEntries: maxEntries, fileMode: fs.FileMode(fileMode), directoryMode: fs.FileMode(directoryMode)}, nil
}

type fsTool struct {
	name       string
	filesystem filesystem.FS
	config     normalizedConfig
}

func (t *fsTool) Definition() tool.Definition {
	description := map[string]string{
		toolRead:   "Read UTF-8 text from a filesystem path.",
		toolWrite:  "Write UTF-8 text to a filesystem path.",
		toolList:   "List entries in a filesystem directory.",
		toolStat:   "Inspect filesystem metadata for a path.",
		toolMkdir:  "Create a filesystem directory and its parents.",
		toolRemove: "Remove a filesystem path.",
		toolRename: "Rename or move a filesystem path.",
	}[t.name]
	schema := objectSchema(t.name)
	return tool.Definition{Name: t.name, Description: description, InputSchema: schema}
}

func (t *fsTool) Invoke(ctx context.Context, call tool.Call) (tool.Result, error) {
	if ctx == nil {
		return tool.Result{}, fmt.Errorf("%s: nil context", t.name)
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if call.Name != "" && call.Name != t.name {
		return tool.Result{}, fmt.Errorf("%s received call for %q: %w", t.name, call.Name, ErrInvalidArguments)
	}
	switch t.name {
	case toolRead:
		var args pathArgs
		if err := decodeObject(call.Arguments, &args); err != nil {
			return tool.Result{}, err
		}
		if err := validatePath(args.Path); err != nil {
			return tool.Result{}, err
		}
		content, err := t.filesystem.ReadFile(ctx, args.Path)
		if err != nil {
			return tool.Result{}, err
		}
		if len(content) > t.config.maxReadBytes {
			return tool.Result{}, fmt.Errorf("read %q: %w", args.Path, ErrResultLimit)
		}
		if !utf8.Valid(content) {
			return tool.Result{}, fmt.Errorf("read %q: %w", args.Path, ErrBinaryContent)
		}
		return tool.Result{Content: string(content)}, nil
	case toolWrite:
		var args writeArgs
		if err := decodeObject(call.Arguments, &args); err != nil {
			return tool.Result{}, err
		}
		if args.Content == nil {
			return tool.Result{}, fmt.Errorf("content is required: %w", ErrInvalidArguments)
		}
		if err := validatePath(args.Path); err != nil {
			return tool.Result{}, err
		}
		if !utf8.ValidString(*args.Content) {
			return tool.Result{}, fmt.Errorf("content: %w", ErrBinaryContent)
		}
		if err := t.filesystem.WriteFile(ctx, args.Path, []byte(*args.Content), t.config.fileMode); err != nil {
			return tool.Result{}, err
		}
		return tool.Result{Content: fmt.Sprintf("wrote %q", args.Path)}, nil
	case toolList:
		var args pathArgs
		if err := decodeObject(call.Arguments, &args); err != nil {
			return tool.Result{}, err
		}
		if err := validatePath(args.Path); err != nil {
			return tool.Result{}, err
		}
		entries, err := t.filesystem.ReadDir(ctx, args.Path)
		if err != nil {
			return tool.Result{}, err
		}
		if len(entries) > t.config.maxListEntries {
			return tool.Result{}, fmt.Errorf("list %q: %w", args.Path, ErrResultLimit)
		}
		result := make([]listEntry, 0, len(entries))
		for _, entry := range entries {
			if entry == nil || !utf8.ValidString(entry.Name()) {
				return tool.Result{}, fmt.Errorf("list %q: invalid entry name: %w", args.Path, ErrInvalidArguments)
			}
		}
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			result = append(result, listEntry{Name: entry.Name(), Type: entryType(entry.Type())})
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return tool.Result{}, err
		}
		return tool.Result{Content: string(encoded)}, nil
	case toolStat:
		var args pathArgs
		if err := decodeObject(call.Arguments, &args); err != nil {
			return tool.Result{}, err
		}
		if err := validatePath(args.Path); err != nil {
			return tool.Result{}, err
		}
		info, err := t.filesystem.Stat(ctx, args.Path)
		if err != nil {
			return tool.Result{}, err
		}
		if info == nil {
			return tool.Result{}, fmt.Errorf("stat %q returned nil metadata", args.Path)
		}
		metadata := statResult{Name: info.Name(), Size: info.Size(), Mode: uint32(info.Mode()), ModifiedAt: info.ModTime().UTC().Format(time.RFC3339Nano), Type: infoType(info.Mode())}
		encoded, err := json.Marshal(metadata)
		if err != nil {
			return tool.Result{}, err
		}
		return tool.Result{Content: string(encoded)}, nil
	case toolMkdir:
		var args pathArgs
		if err := decodeObject(call.Arguments, &args); err != nil {
			return tool.Result{}, err
		}
		if err := validatePath(args.Path); err != nil {
			return tool.Result{}, err
		}
		if err := t.filesystem.MkdirAll(ctx, args.Path, t.config.directoryMode); err != nil {
			return tool.Result{}, err
		}
		return tool.Result{Content: fmt.Sprintf("created directory %q", args.Path)}, nil
	case toolRemove:
		var args pathArgs
		if err := decodeObject(call.Arguments, &args); err != nil {
			return tool.Result{}, err
		}
		if err := validatePath(args.Path); err != nil {
			return tool.Result{}, err
		}
		if err := t.filesystem.Remove(ctx, args.Path); err != nil {
			return tool.Result{}, err
		}
		return tool.Result{Content: fmt.Sprintf("removed %q", args.Path)}, nil
	case toolRename:
		var args renameArgs
		if err := decodeObject(call.Arguments, &args); err != nil {
			return tool.Result{}, err
		}
		if args.Source == nil || args.Destination == nil {
			return tool.Result{}, fmt.Errorf("source and destination are required: %w", ErrInvalidArguments)
		}
		if err := validatePath(*args.Source); err != nil {
			return tool.Result{}, fmt.Errorf("source: %w", err)
		}
		if err := validatePath(*args.Destination); err != nil {
			return tool.Result{}, fmt.Errorf("destination: %w", err)
		}
		if err := t.filesystem.Rename(ctx, *args.Source, *args.Destination); err != nil {
			return tool.Result{}, err
		}
		return tool.Result{Content: fmt.Sprintf("renamed %q to %q", *args.Source, *args.Destination)}, nil
	default:
		return tool.Result{}, fmt.Errorf("unknown filesystem tool %q", t.name)
	}
}

type pathArgs struct {
	Path string `json:"path"`
}
type writeArgs struct {
	Path    string  `json:"path"`
	Content *string `json:"content"`
}
type renameArgs struct {
	Source      *string `json:"source"`
	Destination *string `json:"destination"`
}
type listEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
}
type statResult struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Mode       uint32 `json:"mode"`
	ModifiedAt string `json:"modified_at"`
	Type       string `json:"type"`
}

func objectSchema(name string) json.RawMessage {
	switch name {
	case toolWrite:
		return json.RawMessage(`{"type":"object","additionalProperties":false,"required":["path","content"],"properties":{"path":{"type":"string","minLength":1},"content":{"type":"string"}}}`)
	case toolRename:
		return json.RawMessage(`{"type":"object","additionalProperties":false,"required":["source","destination"],"properties":{"source":{"type":"string","minLength":1},"destination":{"type":"string","minLength":1}}}`)
	}
	return json.RawMessage(`{"type":"object","additionalProperties":false,"required":["path"],"properties":{"path":{"type":"string","minLength":1}}}`)
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

func validatePath(path string) error {
	if path == "" || !utf8.ValidString(path) {
		return fmt.Errorf("path must be a non-empty UTF-8 string: %w", ErrInvalidArguments)
	}
	return nil
}

func entryType(mode fs.FileMode) string {
	if mode&fs.ModeSymlink != 0 {
		return "symlink"
	}
	if mode.IsDir() {
		return "directory"
	}
	if mode.IsRegular() {
		return "file"
	}
	return "other"
}

func infoType(mode fs.FileMode) string { return entryType(mode) }

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

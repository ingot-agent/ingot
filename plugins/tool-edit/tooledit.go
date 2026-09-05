// Package tooledit exposes deterministic exact-text editing over a workspace filesystem.
package tooledit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/ingot-agent/ingot-abi"
	contentprotocol "github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/filesystem"
	"github.com/ingot-agent/sdk/tool"
)

const (
	defaultMaxFileBytes = 1024 * 1024
	toolEdit            = "fs_edit"
)

var (
	// ErrInvalidConfig indicates invalid tool.edit configuration or dependencies.
	ErrInvalidConfig = errors.New("invalid tool.edit config")
	// ErrInvalidArguments indicates malformed or semantically invalid edit arguments.
	ErrInvalidArguments = errors.New("invalid tool.edit arguments")
	// ErrBinaryContent indicates that the target is not valid UTF-8 text.
	ErrBinaryContent = errors.New("binary content is not supported")
	// ErrFileTooLarge indicates that the target exceeds the configured edit bound.
	ErrFileTooLarge = errors.New("edit file exceeds size limit")
	// ErrNoMatch indicates that old_text does not occur in the target file.
	ErrNoMatch = errors.New("edit target not found")
	// ErrAmbiguousMatch indicates that a unique edit matched more than once.
	ErrAmbiguousMatch = errors.New("edit target is ambiguous")
)

// Config bounds files accepted by fs_edit.
type Config struct {
	MaxFileBytes int `toml:"max_file_bytes"`
}

// Dependencies contains the workspace filesystem used by fs_edit.
type Dependencies struct {
	Filesystem filesystem.FS
}

// Exports contains the editing tool.
type Exports struct {
	Tools []tool.Tool
}

type editTool struct {
	filesystem   filesystem.FS
	maxFileBytes int
}

type editArgs struct {
	Path       string  `json:"path"`
	OldText    string  `json:"old_text"`
	NewText    *string `json:"new_text"`
	ReplaceAll bool    `json:"replace_all"`
}

type editResult struct {
	Path         string `json:"path"`
	Replacements int    `json:"replacements"`
	BytesBefore  int    `json:"bytes_before"`
	BytesAfter   int    `json:"bytes_after"`
}

const editSchema = `{"type":"object","additionalProperties":false,"required":["path","old_text","new_text"],"properties":{"path":{"type":"string","minLength":1},"old_text":{"type":"string","minLength":1},"new_text":{"type":"string"},"replace_all":{"type":"boolean","default":false}}}`

// New validates configuration and creates an independent fs_edit tool instance.
func New(ctx context.Context, cfg Config, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, fmt.Errorf("construct tool.edit: %w", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	if isNil(deps.Filesystem) {
		return Exports{}, nil, fmt.Errorf("filesystem dependency is required: %w", ErrInvalidConfig)
	}
	maxFileBytes := cfg.MaxFileBytes
	if maxFileBytes == 0 {
		maxFileBytes = defaultMaxFileBytes
	}
	if maxFileBytes < 1 {
		return Exports{}, nil, fmt.Errorf("max_file_bytes must be positive: %w", ErrInvalidConfig)
	}
	return Exports{Tools: []tool.Tool{&editTool{filesystem: deps.Filesystem, maxFileBytes: maxFileBytes}}}, nil, nil
}

func (t *editTool) Definition() tool.Definition {
	return tool.Definition{
		Name: toolEdit,
		Description: "Edit an existing UTF-8 text file by replacing an exact text fragment. " +
			"The old_text must match exactly and, unless replace_all is true, must occur exactly once. " +
			"Read the file first when its current contents are uncertain.",
		InputSchema: json.RawMessage(editSchema),
	}
}

func (t *editTool) Invoke(ctx context.Context, call tool.Call) (tool.Result, error) {
	if ctx == nil {
		return tool.Result{}, fmt.Errorf("%s: nil context", toolEdit)
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if call.Name != "" && call.Name != toolEdit {
		return tool.Result{}, fmt.Errorf("%s received call for %q: %w", toolEdit, call.Name, ErrInvalidArguments)
	}

	var args editArgs
	if err := decodeObject(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if err := validateArgs(args); err != nil {
		return tool.Result{}, err
	}

	info, err := t.filesystem.Stat(ctx, args.Path)
	if err != nil {
		return tool.Result{}, fmt.Errorf("edit %q: stat: %w", args.Path, err)
	}
	if info == nil {
		return tool.Result{}, fmt.Errorf("edit %q: stat returned nil metadata", args.Path)
	}
	if !info.Mode().IsRegular() {
		return tool.Result{}, fmt.Errorf("edit %q: not a regular file: %w", args.Path, fs.ErrInvalid)
	}
	if info.Size() > int64(t.maxFileBytes) {
		return tool.Result{}, fileTooLargeError(args.Path, info.Size(), t.maxFileBytes)
	}
	mode := info.Mode().Perm()

	current, err := t.filesystem.ReadFile(ctx, args.Path)
	if err != nil {
		return tool.Result{}, fmt.Errorf("edit %q: read: %w", args.Path, err)
	}
	if len(current) > t.maxFileBytes {
		return tool.Result{}, fileTooLargeError(args.Path, int64(len(current)), t.maxFileBytes)
	}
	if !utf8.Valid(current) {
		return tool.Result{}, fmt.Errorf("edit %q: %w", args.Path, ErrBinaryContent)
	}

	updated, replacements, err := applyEdit(string(current), args.OldText, *args.NewText, args.ReplaceAll, t.maxFileBytes)
	if err != nil {
		return tool.Result{}, fmt.Errorf("edit %q: %w", args.Path, err)
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if err := t.filesystem.WriteFile(ctx, args.Path, []byte(updated), mode); err != nil {
		return tool.Result{}, fmt.Errorf("edit %q: write: %w", args.Path, err)
	}

	encoded, err := json.Marshal(editResult{Path: args.Path, Replacements: replacements, BytesBefore: len(current), BytesAfter: len(updated)})
	if err != nil {
		return tool.Result{}, fmt.Errorf("encode edit result: %w", err)
	}
	return tool.Result{Content: contentprotocol.FromText(string(encoded))}, nil
}

func validateArgs(args editArgs) error {
	if args.Path == "" || !utf8.ValidString(args.Path) {
		return fmt.Errorf("path must be a non-empty UTF-8 string: %w", ErrInvalidArguments)
	}
	if args.OldText == "" || !utf8.ValidString(args.OldText) {
		return fmt.Errorf("old_text must be a non-empty UTF-8 string: %w", ErrInvalidArguments)
	}
	if args.NewText == nil {
		return fmt.Errorf("new_text is required: %w", ErrInvalidArguments)
	}
	if !utf8.ValidString(*args.NewText) {
		return fmt.Errorf("new_text must be a UTF-8 string: %w", ErrInvalidArguments)
	}
	if args.OldText == *args.NewText {
		return fmt.Errorf("old_text and new_text must differ: %w", ErrInvalidArguments)
	}
	return nil
}

func applyEdit(content, oldText, newText string, replaceAll bool, maxBytes int) (string, int, error) {
	count := strings.Count(content, oldText)
	if count == 0 {
		return "", 0, ErrNoMatch
	}
	if !replaceAll && count != 1 {
		return "", 0, fmt.Errorf("old_text matched %d locations: %w", count, ErrAmbiguousMatch)
	}
	replacements := count
	if !replaceAll {
		replacements = 1
	}
	if replacementExceedsLimit(len(content), len(oldText), len(newText), replacements, maxBytes) {
		return "", 0, fmt.Errorf("replacement result exceeds limit %d: %w", maxBytes, ErrFileTooLarge)
	}
	if replaceAll {
		return strings.ReplaceAll(content, oldText, newText), count, nil
	}
	return strings.Replace(content, oldText, newText, 1), 1, nil
}

func replacementExceedsLimit(contentBytes, oldBytes, newBytes, replacements, maxBytes int) bool {
	if contentBytes > maxBytes {
		return true
	}
	if newBytes <= oldBytes {
		return false
	}
	growthPerReplacement := newBytes - oldBytes
	return replacements > (maxBytes-contentBytes)/growthPerReplacement
}

func fileTooLargeError(path string, size int64, limit int) error {
	return fmt.Errorf("edit %q: file size %d exceeds limit %d: %w", path, size, limit, ErrFileTooLarge)
}

func decodeObject(raw json.RawMessage, target any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return fmt.Errorf("arguments are required: %w", ErrInvalidArguments)
	}
	if !utf8.Valid(raw) {
		return fmt.Errorf("arguments must be valid UTF-8: %w", ErrInvalidArguments)
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

package tooledit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/tool"
	"github.com/ingot-agent/sdk/workspace"
)

// readTool reads a workspace-relative UTF-8 text file and returns its content.
type readTool struct {
	config    normalizedConfig
	workspace workspace.Resolver
}

type readArguments struct {
	Path *string `json:"path"`
}

// Definition implements tool.Tool.
func (t *readTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        readToolName,
		Description: "Read a workspace-relative UTF-8 text file and return its content.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["path"],"properties":{"path":{"type":"string","minLength":1}}}`),
	}
}

// Invoke implements tool.Tool.
func (t *readTool) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	if ctx == nil {
		return tool.Result{}, fmt.Errorf("read_file: nil context")
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	call := invocation.Call
	if call.Name != "" && call.Name != readToolName {
		return tool.Result{}, fmt.Errorf("call name %q: %w", call.Name, ErrInvalidArguments)
	}
	binding, err := t.workspace.Resolve(ctx, invocation.Scope)
	if err != nil {
		return tool.Result{}, fmt.Errorf("read_file resolve workspace for session %q: %w", invocation.Scope.SessionID, err)
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	var args readArguments
	if err := decodeObject(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if args.Path == nil || *args.Path == "" || !utf8.ValidString(*args.Path) {
		return tool.Result{}, fmt.Errorf("path must be a non-empty UTF-8 string: %w", ErrInvalidArguments)
	}
	target, err := resolveTarget(binding.Root, *args.Path)
	if err != nil {
		if errors.Is(err, ErrUnsafePath) || errors.Is(err, ErrInvalidArguments) {
			return t.businessResult(ctx, fmt.Sprintf("read_file error: %v", err))
		}
		return tool.Result{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return t.businessResult(ctx, fmt.Sprintf("read_file error: file not found: %s", *args.Path))
		}
		return tool.Result{}, fmt.Errorf("read_file stat %q: %w", *args.Path, err)
	}
	if info.IsDir() {
		return t.businessResult(ctx, fmt.Sprintf("read_file error: path is a directory: %s", *args.Path))
	}
	if info.Size() > int64(t.config.maxFileBytes) {
		return t.businessResult(ctx, fmt.Sprintf("read_file error: file exceeds %d bytes: %s", t.config.maxFileBytes, *args.Path))
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return tool.Result{}, fmt.Errorf("read_file read %q: %w", *args.Path, err)
	}
	if !utf8.Valid(data) {
		return t.businessResult(ctx, fmt.Sprintf("read_file error: file is not valid UTF-8: %s", *args.Path))
	}
	return tool.Result{Content: content.FromText(string(data))}, nil
}

// businessResult reports a known read_file failure as a normal tool result so
// the model can read the reason and continue, mirroring the edit tool.
func (t *readTool) businessResult(ctx context.Context, message string) (tool.Result, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return tool.Result{}, ctxErr
	}
	return tool.Result{Content: content.FromText(message)}, nil
}

var _ tool.Tool = (*readTool)(nil)

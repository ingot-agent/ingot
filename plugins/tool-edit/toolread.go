package tooledit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
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
	Path      *string `json:"path"`
	StartLine *int    `json:"start_line"`
	EndLine   *int    `json:"end_line"`
}

// Definition implements tool.Tool.
func (t *readTool) Definition() tool.Definition {
	return tool.Definition{
		Name: readToolName,
		Description: "Read a workspace-relative UTF-8 text file and return its content. " +
			"By default the whole file is returned. Pass start_line and/or end_line " +
			"(1-based, inclusive) to read only a line range. " +
			"Example: {\"path\":\"src/main.go\",\"start_line\":10,\"end_line\":20} returns lines 10-20.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["path"],"properties":{"path":{"type":"string","minLength":1},"start_line":{"type":"integer","minimum":1},"end_line":{"type":"integer","minimum":1}}}`),
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
	text := string(data)
	if args.StartLine == nil && args.EndLine == nil {
		return tool.Result{Content: content.FromText(text)}, nil
	}
	lines, err := selectLines(text, args.StartLine, args.EndLine)
	if err != nil {
		return t.businessResult(ctx, fmt.Sprintf("read_file error: %v", err))
	}
	return tool.Result{Content: content.FromText(strings.Join(lines, "\n"))}, nil
}

// selectLines extracts an inclusive 1-based line range from a UTF-8 text
// value. A nil bound means "open" in that direction. Trailing newline handling
// matches strings.Split semantics: the final empty fragment after a trailing
// newline is not treated as an extra line.
func selectLines(text string, startLine, endLine *int) ([]string, error) {
	if startLine != nil && *startLine < 1 {
		return nil, fmt.Errorf("start_line must be >= 1")
	}
	if endLine != nil && *endLine < 1 {
		return nil, fmt.Errorf("end_line must be >= 1")
	}
	if startLine != nil && endLine != nil && *startLine > *endLine {
		return nil, fmt.Errorf("start_line (%d) must not exceed end_line (%d)", *startLine, *endLine)
	}
	parts := strings.Split(text, "\n")
	// A trailing newline produces one empty trailing fragment; drop it so it is
	// not counted as an actual line for range math.
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	total := len(parts)
	start := 1
	if startLine != nil {
		start = *startLine
	}
	end := total
	if endLine != nil {
		end = *endLine
	}
	if start > total {
		return nil, fmt.Errorf("start_line (%d) exceeds file line count (%d)", start, total)
	}
	if end > total {
		end = total
	}
	return parts[start-1 : end], nil
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

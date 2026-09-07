package tooledit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/tool"
	"github.com/ingot-agent/sdk/workspace"
)

// errScanLimit indicates the recursive search hit its total byte budget.

// searchTool searches workspace-relative UTF-8 text files for a string.
type searchTool struct {
	config    normalizedConfig
	workspace workspace.Resolver
}

type searchArguments struct {
	Pattern *string `json:"pattern"`
	Path    string  `json:"path"`
	Glob    string  `json:"glob"`
}

// Definition implements tool.Tool.
func (t *searchTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        searchToolName,
		Description: "Search workspace-relative UTF-8 text files for an exact substring and report matching lines.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["pattern"],"properties":{"pattern":{"type":"string","minLength":1},"path":{"type":"string"},"glob":{"type":"string"}}}`),
	}
}

// Invoke implements tool.Tool.
func (t *searchTool) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	if ctx == nil {
		return tool.Result{}, fmt.Errorf("search: nil context")
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	call := invocation.Call
	if call.Name != "" && call.Name != searchToolName {
		return tool.Result{}, fmt.Errorf("call name %q: %w", call.Name, ErrInvalidArguments)
	}
	binding, err := t.workspace.Resolve(ctx, invocation.Scope)
	if err != nil {
		return tool.Result{}, fmt.Errorf("search resolve workspace for session %q: %w", invocation.Scope.SessionID, err)
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	var args searchArguments
	if err := decodeObject(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if args.Pattern == nil || *args.Pattern == "" || !utf8.ValidString(*args.Pattern) {
		return tool.Result{}, fmt.Errorf("pattern must be a non-empty UTF-8 string: %w", ErrInvalidArguments)
	}
	if args.Path != "" && !utf8.ValidString(args.Path) {
		return tool.Result{}, fmt.Errorf("path must be valid UTF-8: %w", ErrInvalidArguments)
	}
	if args.Glob != "" && !utf8.ValidString(args.Glob) {
		return tool.Result{}, fmt.Errorf("glob must be valid UTF-8: %w", ErrInvalidArguments)
	}
	searchRoot := binding.Root
	if args.Path != "" {
		searchRoot, err = resolveTarget(binding.Root, args.Path)
		if err != nil {
			if errors.Is(err, ErrUnsafePath) || errors.Is(err, ErrInvalidArguments) {
				return t.businessResult(ctx, fmt.Sprintf("search error: %v", err))
			}
			return tool.Result{}, err
		}
	}
	info, err := os.Stat(searchRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return t.businessResult(ctx, fmt.Sprintf("search error: path not found: %s", args.Path))
		}
		return tool.Result{}, fmt.Errorf("search stat %q: %w", args.Path, err)
	}
	if !info.IsDir() {
		return t.businessResult(ctx, fmt.Sprintf("search error: path is not a directory: %s", args.Path))
	}
	matches, truncated, err := t.search(ctx, searchRoot, *args.Pattern, args.Glob)
	if err != nil {
		return tool.Result{}, err
	}
	results := t.format(matches, *args.Pattern, truncated)
	return tool.Result{Content: content.FromText(results)}, nil
}

type match struct {
	relative string
	line     int
	text     string
}

func (t *searchTool) format(matches []match, pattern string, truncated bool) string {
	if len(matches) == 0 {
		if truncated {
			return fmt.Sprintf("search: no matches for %q (scan limit reached)", pattern)
		}
		return fmt.Sprintf("search: no matches for %q", pattern)
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("search: %d match(es) for %q\n", len(matches), pattern))
	for _, m := range matches {
		b.WriteString(fmt.Sprintf("%s:%d: %s\n", filepath.ToSlash(m.relative), m.line, m.text))
	}
	if truncated {
		b.WriteString("search: results truncated at scan limit\n")
	}
	return b.String()
}

// search recursively walks searchRoot, reading regular UTF-8 text files, and
// returns matches in deterministic sorted file order. scanLimit is not a hard
// error: if the cumulative byte budget is crossed, matching stops and the
// truncated flag is set so callers can report the boundary.
func (t *searchTool) search(ctx context.Context, searchRoot, pattern, glob string) ([]match, bool, error) {
	var matches []match
	scanned := 0
	truncated := false
	walkErr := filepath.WalkDir(searchRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if entry.IsDir() {
			// Skip hidden directories (dot-prefixed) except the root itself.
			if path != searchRoot && strings.HasPrefix(entry.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		// Skip hidden files (dot-prefixed) and other non-text content.
		if strings.HasPrefix(entry.Name(), ".") {
			return nil
		}
		if glob != "" {
			ok, err := filepath.Match(glob, entry.Name())
			if err != nil {
				return err
			}
			if !ok {
				return nil
			}
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > int64(t.config.maxFileBytes) {
			// Skip oversized files rather than silently truncating their
			// contents; the per-file bound is a known limit.
			return nil
		}
		scanned += int(info.Size())
		if scanned > t.config.maxScanBytes {
			truncated = true
			return fs.SkipAll
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !utf8.Valid(data) {
			return nil
		}
		rel, err := filepath.Rel(searchRoot, path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, pattern) {
				matches = append(matches, match{relative: rel, line: i + 1, text: line})
			}
		}
		return nil
	})
	if walkErr != nil {
		// Preserve context cancellation and real I/O errors; only the scan
		// budget is reported as a normal business boundary via truncated.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, truncated, ctxErr
		}
		return nil, truncated, walkErr
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].relative != matches[j].relative {
			return matches[i].relative < matches[j].relative
		}
		return matches[i].line < matches[j].line
	})
	return matches, truncated, nil
}

// businessResult reports a known search failure as a normal tool result so the
// model can read the reason and continue.
func (t *searchTool) businessResult(ctx context.Context, message string) (tool.Result, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return tool.Result{}, ctxErr
	}
	return tool.Result{Content: content.FromText(message)}, nil
}

var _ tool.Tool = (*searchTool)(nil)

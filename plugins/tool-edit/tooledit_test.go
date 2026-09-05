package tooledit

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"testing"
	"time"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/tool"
)

type fakeFS struct {
	readData   []byte
	info       fs.FileInfo
	statErr    error
	readErr    error
	writeErr   error
	onRead     func()
	statCalls  int
	readCalls  int
	writeCalls int
	lastCtx    context.Context
	written    string
	mode       fs.FileMode
}

func (f *fakeFS) ReadFile(ctx context.Context, _ string) ([]byte, error) {
	f.lastCtx = ctx
	f.readCalls++
	if f.onRead != nil {
		f.onRead()
	}
	return append([]byte(nil), f.readData...), f.readErr
}

func (f *fakeFS) WriteFile(ctx context.Context, _ string, data []byte, mode fs.FileMode) error {
	f.lastCtx = ctx
	f.writeCalls++
	f.written = string(data)
	f.mode = mode
	return f.writeErr
}

func (f *fakeFS) ReadDir(context.Context, string) ([]fs.DirEntry, error) { return nil, nil }

func (f *fakeFS) Stat(ctx context.Context, _ string) (fs.FileInfo, error) {
	f.lastCtx = ctx
	f.statCalls++
	return f.info, f.statErr
}

func (f *fakeFS) MkdirAll(context.Context, string, fs.FileMode) error { return nil }
func (f *fakeFS) Remove(context.Context, string) error                { return nil }
func (f *fakeFS) Rename(context.Context, string, string) error        { return nil }

type fakeInfo struct {
	name string
	size int64
	mode fs.FileMode
}

func (i fakeInfo) Name() string       { return i.name }
func (i fakeInfo) Size() int64        { return i.size }
func (i fakeInfo) Mode() fs.FileMode  { return i.mode }
func (i fakeInfo) ModTime() time.Time { return time.Time{} }
func (i fakeInfo) IsDir() bool        { return i.mode.IsDir() }
func (i fakeInfo) Sys() any           { return nil }

func regularInfo(size int64, mode fs.FileMode) fs.FileInfo {
	return fakeInfo{name: "file.txt", size: size, mode: mode}
}

func newTestTool(t *testing.T, fake *fakeFS, cfg Config) tool.Tool {
	t.Helper()
	if fake.info == nil {
		fake.info = regularInfo(int64(len(fake.readData)), 0o640)
	}
	exports, cleanup, err := New(context.Background(), cfg, Dependencies{Filesystem: fake})
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		t.Fatal("tool.edit must not allocate cleanup resources")
	}
	if len(exports.Tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(exports.Tools))
	}
	return exports.Tools[0]
}

func invoke(candidate tool.Tool, arguments []byte) (tool.Result, error) {
	return candidate.Invoke(context.Background(), tool.Call{Name: toolEdit, Arguments: arguments})
}

func resultText(result tool.Result) string {
	value, _ := content.TextOnly(result.Content)
	return value
}

func TestNewAndDefinition(t *testing.T) {
	fake := &fakeFS{readData: []byte("old")}
	candidate := newTestTool(t, fake, Config{})
	if candidate.(*editTool).maxFileBytes != defaultMaxFileBytes {
		t.Fatalf("default max file bytes = %d", candidate.(*editTool).maxFileBytes)
	}
	definition := candidate.Definition()
	if definition.Name != toolEdit || definition.Description == "" || !json.Valid(definition.InputSchema) {
		t.Fatalf("invalid definition: %#v", definition)
	}
	definition.InputSchema[0] = 'X'
	if candidate.Definition().InputSchema[0] == 'X' {
		t.Fatal("definition schema aliases mutable package data")
	}

	if _, _, err := New(nil, Config{}, Dependencies{Filesystem: fake}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("nil context error = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := New(cancelled, Config{}, Dependencies{Filesystem: fake}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context error = %v", err)
	}
	if _, _, err := New(context.Background(), Config{}, Dependencies{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("nil filesystem error = %v", err)
	}
	var typedNil *fakeFS
	if _, _, err := New(context.Background(), Config{}, Dependencies{Filesystem: typedNil}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("typed nil filesystem error = %v", err)
	}
	if _, _, err := New(context.Background(), Config{MaxFileBytes: -1}, Dependencies{Filesystem: fake}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("negative limit error = %v", err)
	}
}

func TestEditBehaviors(t *testing.T) {
	tests := []struct {
		name       string
		current    string
		arguments  string
		want       string
		wantResult string
	}{
		{"unique", "before old after", `{"path":"file.txt","old_text":"old","new_text":"new"}`, "before new after", `{"path":"file.txt","replacements":1,"bytes_before":16,"bytes_after":16}`},
		{"delete", "a-delete-b", `{"path":"file.txt","old_text":"-delete","new_text":""}`, "a-b", `{"path":"file.txt","replacements":1,"bytes_before":10,"bytes_after":3}`},
		{"multiline unicode", "开始\nold\n结束", `{"path":"file.txt","old_text":"old\n结束","new_text":"new\n世界"}`, "开始\nnew\n世界", `{"path":"file.txt","replacements":1,"bytes_before":17,"bytes_after":17}`},
		{"replace all", "old old", `{"path":"file.txt","old_text":"old","new_text":"n","replace_all":true}`, "n n", `{"path":"file.txt","replacements":2,"bytes_before":7,"bytes_after":3}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeFS{readData: []byte(test.current)}
			result, err := invoke(newTestTool(t, fake, Config{}), []byte(test.arguments))
			if err != nil {
				t.Fatal(err)
			}
			if fake.written != test.want || fake.writeCalls != 1 || fake.mode != 0o640 {
				t.Fatalf("write data=%q calls=%d mode=%v", fake.written, fake.writeCalls, fake.mode)
			}
			if got := resultText(result); got != test.wantResult {
				t.Fatalf("result = %s, want %s", got, test.wantResult)
			}
		})
	}
}

func TestArgumentValidationHasNoFilesystemSideEffects(t *testing.T) {
	tests := []struct {
		name      string
		arguments []byte
	}{
		{"empty", nil},
		{"malformed", []byte(`{"path":`)},
		{"invalid utf8", []byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}},
		{"missing path", []byte(`{"old_text":"a","new_text":"b"}`)},
		{"missing old", []byte(`{"path":"x","new_text":"b"}`)},
		{"missing new", []byte(`{"path":"x","old_text":"a"}`)},
		{"unknown", []byte(`{"path":"x","old_text":"a","new_text":"b","extra":1}`)},
		{"trailing", []byte(`{"path":"x","old_text":"a","new_text":"b"}{}`)},
		{"wrong boolean", []byte(`{"path":"x","old_text":"a","new_text":"b","replace_all":"yes"}`)},
		{"no-op", []byte(`{"path":"x","old_text":"a","new_text":"a"}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeFS{readData: []byte("a")}
			_, err := invoke(newTestTool(t, fake, Config{}), test.arguments)
			if !errors.Is(err, ErrInvalidArguments) {
				t.Fatalf("error = %v", err)
			}
			if fake.statCalls != 0 || fake.readCalls != 0 || fake.writeCalls != 0 {
				t.Fatalf("filesystem calls stat=%d read=%d write=%d", fake.statCalls, fake.readCalls, fake.writeCalls)
			}
		})
	}
}

func TestFileAndMatchFailuresDoNotWrite(t *testing.T) {
	statFailure := errors.New("stat failure")
	readFailure := errors.New("read failure")
	tests := []struct {
		name    string
		fake    *fakeFS
		cfg     Config
		wantErr error
	}{
		{"stat", &fakeFS{readData: []byte("old"), statErr: statFailure}, Config{}, statFailure},
		{"directory", &fakeFS{readData: []byte("old"), info: fakeInfo{name: "x", mode: fs.ModeDir | 0o755}}, Config{}, fs.ErrInvalid},
		{"stat oversized", &fakeFS{readData: []byte("old"), info: regularInfo(11, 0o600)}, Config{MaxFileBytes: 10}, ErrFileTooLarge},
		{"read oversized", &fakeFS{readData: []byte("01234567890"), info: regularInfo(10, 0o600)}, Config{MaxFileBytes: 10}, ErrFileTooLarge},
		{"read", &fakeFS{readData: []byte("old"), readErr: readFailure}, Config{}, readFailure},
		{"binary", &fakeFS{readData: []byte{0xff}}, Config{}, ErrBinaryContent},
		{"no match", &fakeFS{readData: []byte("other")}, Config{}, ErrNoMatch},
		{"ambiguous", &fakeFS{readData: []byte("old old")}, Config{}, ErrAmbiguousMatch},
	}
	arguments := []byte(`{"path":"x","old_text":"old","new_text":"new"}`)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := invoke(newTestTool(t, test.fake, test.cfg), arguments)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want errors.Is(%v)", err, test.wantErr)
			}
			if test.fake.writeCalls != 0 {
				t.Fatalf("write calls = %d", test.fake.writeCalls)
			}
		})
	}
}

func TestNilMetadataWriteErrorAndContext(t *testing.T) {
	fake := &fakeFS{readData: []byte("old")}
	exports, _, err := New(context.Background(), Config{}, Dependencies{Filesystem: fake})
	if err != nil {
		t.Fatal(err)
	}
	_, err = invoke(exports.Tools[0], []byte(`{"path":"x","old_text":"old","new_text":"new"}`))
	if err == nil || fake.writeCalls != 0 {
		t.Fatalf("nil metadata error=%v writes=%d", err, fake.writeCalls)
	}

	writeFailure := errors.New("write failure")
	fake = &fakeFS{readData: []byte("old"), writeErr: writeFailure}
	candidate := newTestTool(t, fake, Config{})
	_, err = invoke(candidate, []byte(`{"path":"x","old_text":"old","new_text":"new"}`))
	if !errors.Is(err, writeFailure) || fake.writeCalls != 1 {
		t.Fatalf("write error=%v calls=%d", err, fake.writeCalls)
	}

	if _, err := candidate.Invoke(nil, tool.Call{}); err == nil {
		t.Fatal("nil invoke context must fail")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	before := fake.writeCalls
	_, err = candidate.Invoke(cancelled, tool.Call{})
	if !errors.Is(err, context.Canceled) || fake.writeCalls != before {
		t.Fatalf("cancelled error=%v writes=%d", err, fake.writeCalls)
	}
	_, err = candidate.Invoke(context.Background(), tool.Call{Name: "other", Arguments: []byte(`{}`)})
	if !errors.Is(err, ErrInvalidArguments) {
		t.Fatalf("wrong name error = %v", err)
	}
}

func TestCancellationAfterReadPreventsWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fake := &fakeFS{readData: []byte("old"), onRead: cancel}
	candidate := newTestTool(t, fake, Config{})
	_, err := candidate.Invoke(ctx, tool.Call{Name: toolEdit, Arguments: []byte(`{"path":"x","old_text":"old","new_text":"new"}`)})
	if !errors.Is(err, context.Canceled) || fake.writeCalls != 0 {
		t.Fatalf("error=%v writes=%d", err, fake.writeCalls)
	}
}

func TestApplyEditUsesNonOverlappingMatches(t *testing.T) {
	updated, count, err := applyEdit("aaa", "aa", "b", false, 3)
	if err != nil || updated != "ba" || count != 1 {
		t.Fatalf("applyEdit = %q, %d, %v", updated, count, err)
	}
}

func TestReplacementSizeIsBoundedBeforeAllocation(t *testing.T) {
	tests := []struct {
		name       string
		current    string
		arguments  string
		limit      int
		wantErr    bool
		wantOutput string
	}{
		{"single oversized", "old", `{"path":"x","old_text":"old","new_text":"12345"}`, 4, true, ""},
		{"replace all oversized", "aaaa", `{"path":"x","old_text":"a","new_text":"1234","replace_all":true}`, 10, true, ""},
		{"exact boundary", "aa", `{"path":"x","old_text":"a","new_text":"12","replace_all":true}`, 4, false, "1212"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeFS{readData: []byte(test.current)}
			_, err := invoke(newTestTool(t, fake, Config{MaxFileBytes: test.limit}), []byte(test.arguments))
			if test.wantErr {
				if !errors.Is(err, ErrFileTooLarge) || fake.writeCalls != 0 {
					t.Fatalf("error=%v writes=%d", err, fake.writeCalls)
				}
				return
			}
			if err != nil || fake.written != test.wantOutput || fake.writeCalls != 1 {
				t.Fatalf("error=%v output=%q writes=%d", err, fake.written, fake.writeCalls)
			}
		})
	}
}

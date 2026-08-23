package toolfs

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"testing"
	"time"

	"github.com/ingot-agent/sdk/tool"
)

type fakeFS struct {
	readData []byte
	entries  []fs.DirEntry
	info     fs.FileInfo
	lastCtx  context.Context
	written  string
	mode     fs.FileMode
}

func (f *fakeFS) ReadFile(ctx context.Context, _ string) ([]byte, error) {
	f.lastCtx = ctx
	return append([]byte(nil), f.readData...), nil
}
func (f *fakeFS) WriteFile(ctx context.Context, _ string, data []byte, mode fs.FileMode) error {
	f.lastCtx, f.written, f.mode = ctx, string(data), mode
	return nil
}
func (f *fakeFS) ReadDir(ctx context.Context, _ string) ([]fs.DirEntry, error) {
	f.lastCtx = ctx
	return append([]fs.DirEntry(nil), f.entries...), nil
}
func (f *fakeFS) Stat(ctx context.Context, _ string) (fs.FileInfo, error) {
	f.lastCtx = ctx
	return f.info, nil
}
func (f *fakeFS) MkdirAll(ctx context.Context, _ string, _ fs.FileMode) error {
	f.lastCtx = ctx
	return nil
}
func (f *fakeFS) Remove(ctx context.Context, _ string) error    { f.lastCtx = ctx; return nil }
func (f *fakeFS) Rename(ctx context.Context, _, _ string) error { f.lastCtx = ctx; return nil }

type fakeEntry struct {
	name string
	mode fs.FileMode
}

func (e fakeEntry) Name() string               { return e.name }
func (e fakeEntry) IsDir() bool                { return e.mode.IsDir() }
func (e fakeEntry) Type() fs.FileMode          { return e.mode }
func (e fakeEntry) Info() (fs.FileInfo, error) { return fakeInfo{name: e.name, mode: e.mode}, nil }

type fakeInfo struct {
	name     string
	size     int64
	mode     fs.FileMode
	modified time.Time
}

func (i fakeInfo) Name() string       { return i.name }
func (i fakeInfo) Size() int64        { return i.size }
func (i fakeInfo) Mode() fs.FileMode  { return i.mode }
func (i fakeInfo) ModTime() time.Time { return i.modified }
func (i fakeInfo) IsDir() bool        { return i.mode.IsDir() }
func (i fakeInfo) Sys() any           { return nil }

func TestNewExportsStableDefinitionsAndOperations(t *testing.T) {
	fake := &fakeFS{
		readData: []byte("hello"),
		entries:  []fs.DirEntry{fakeEntry{name: "z.go"}, fakeEntry{name: "a", mode: fs.ModeDir}, fakeEntry{name: "link", mode: fs.ModeSymlink}},
		info:     fakeInfo{name: "hello.txt", size: 5, mode: 0o644, modified: time.Date(2025, 1, 2, 3, 4, 5, 6, time.FixedZone("x", 3600))},
	}
	exports, _, err := New(context.Background(), Config{MaxReadBytes: 10, MaxListEntries: 5}, Dependencies{Filesystem: fake})
	if err != nil {
		t.Fatal(err)
	}
	wantNames := []string{"fs.read", "fs.write", "fs.list", "fs.stat", "fs.mkdir", "fs.remove", "fs.rename"}
	for i, candidate := range exports.Tools {
		definition := candidate.Definition()
		if definition.Name != wantNames[i] {
			t.Fatalf("tool %d = %q, want %q", i, definition.Name, wantNames[i])
		}
		if !json.Valid(definition.InputSchema) {
			t.Fatalf("%s schema is invalid JSON", definition.Name)
		}
	}
	ctx := context.WithValue(context.Background(), struct{}{}, "marker")
	result, err := exports.Tools[0].Invoke(ctx, tool.Call{Name: "fs.read", Arguments: []byte("{\"path\":\"hello.txt\"}")})
	if err != nil || result.Content != "hello" {
		t.Fatalf("read = %#v, %v", result, err)
	}
	if fake.lastCtx != ctx {
		t.Fatal("filesystem did not receive the original context")
	}
	result, err = exports.Tools[1].Invoke(ctx, tool.Call{Name: "fs.write", Arguments: []byte("{\"path\":\"out.txt\",\"content\":\"世界\"}")})
	if err != nil || result.Content != "wrote \"out.txt\"" || fake.written != "世界" || fake.mode != 0o644 {
		t.Fatalf("write = %#v, %v, data=%q mode=%v", result, err, fake.written, fake.mode)
	}
	result, err = exports.Tools[2].Invoke(ctx, tool.Call{Name: "fs.list", Arguments: []byte("{\"path\":\".\"}")})
	if err != nil || result.Content != "[{\"name\":\"a\",\"type\":\"directory\"},{\"name\":\"link\",\"type\":\"symlink\"},{\"name\":\"z.go\",\"type\":\"file\"}]" {
		t.Fatalf("list = %q, %v", result.Content, err)
	}
	result, err = exports.Tools[3].Invoke(ctx, tool.Call{Name: "fs.stat", Arguments: []byte("{\"path\":\"hello.txt\"}")})
	if err != nil || result.Content != "{\"name\":\"hello.txt\",\"size\":5,\"mode\":420,\"modified_at\":\"2025-01-02T02:04:05.000000006Z\",\"type\":\"file\"}" {
		t.Fatalf("stat = %q, %v", result.Content, err)
	}
}

func TestReadRejectsBinaryAndListLimitWithoutPartialResult(t *testing.T) {
	fake := &fakeFS{readData: []byte{0xff}, entries: []fs.DirEntry{fakeEntry{name: "a"}, fakeEntry{name: "b"}}}
	exports, _, err := New(context.Background(), Config{MaxReadBytes: 10, MaxListEntries: 1}, Dependencies{Filesystem: fake})
	if err != nil {
		t.Fatal(err)
	}
	_, err = exports.Tools[0].Invoke(context.Background(), tool.Call{Arguments: []byte("{\"path\":\"x\"}")})
	if !errors.Is(err, ErrBinaryContent) {
		t.Fatalf("binary error = %v", err)
	}
	_, err = exports.Tools[2].Invoke(context.Background(), tool.Call{Arguments: []byte("{\"path\":\".\"}")})
	if !errors.Is(err, ErrResultLimit) {
		t.Fatalf("list limit error = %v", err)
	}
}

func TestArgumentsAreStrict(t *testing.T) {
	exports, _, err := New(context.Background(), Config{}, Dependencies{Filesystem: &fakeFS{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = exports.Tools[0].Invoke(context.Background(), tool.Call{Arguments: []byte("{\"path\":\"x\",\"extra\":1}")})
	if !errors.Is(err, ErrInvalidArguments) {
		t.Fatalf("unknown field error = %v", err)
	}
	_, err = exports.Tools[1].Invoke(context.Background(), tool.Call{Arguments: []byte("{\"path\":\"x\"}")})
	if !errors.Is(err, ErrInvalidArguments) {
		t.Fatalf("missing content error = %v", err)
	}
}

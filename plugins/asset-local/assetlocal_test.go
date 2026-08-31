package assetlocal

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/asset"
)

type testScope string

func (s testScope) Dir() string { return string(s) }

var _ state.Scope = testScope("")

func newStore(t *testing.T, root string, cfg Config) *store {
	t.Helper()
	exports, _, err := New(context.Background(), cfg, Dependencies{State: testScope(root)})
	if err != nil {
		t.Fatal(err)
	}
	return exports.Store.(*store)
}

func TestPutOpenStatAndRestart(t *testing.T) {
	root := t.TempDir()
	created := newStore(t, root, Config{})
	value := []byte("immutable")
	reference, info, err := created.Put(context.Background(), asset.PutRequest{Body: bytes.NewReader(value), Size: uint64(len(value))})
	if err != nil {
		t.Fatal(err)
	}
	if reference.ID == "" || info.Size != uint64(len(value)) {
		t.Fatalf("reference=%#v info=%#v", reference, info)
	}
	stat, err := created.Stat(context.Background(), reference)
	if err != nil || stat != info {
		t.Fatalf("stat=%#v err=%v", stat, err)
	}
	body, err := created.Open(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	raw, readErr := io.ReadAll(body)
	closeErr := body.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(raw, value) {
		t.Fatalf("read=%q readErr=%v closeErr=%v", raw, readErr, closeErr)
	}

	restarted := newStore(t, root, Config{})
	reopened, err := restarted.Open(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	raw, _ = io.ReadAll(reopened)
	if !bytes.Equal(raw, value) {
		t.Fatalf("restarted read=%q", raw)
	}
}

func TestPutValidatesDeclaredSizeAndLimitsBeforeReading(t *testing.T) {
	created := newStore(t, t.TempDir(), Config{MaxObjectBytes: 3, MaxTotalBytes: 3})
	reader := &countingReader{Reader: bytes.NewReader([]byte("four"))}
	if _, _, err := created.Put(context.Background(), asset.PutRequest{Body: reader, Size: 4}); !errors.Is(err, ErrObjectLimit) {
		t.Fatalf("object limit error=%v", err)
	}
	if reader.reads != 0 {
		t.Fatalf("oversized body was read %d times", reader.reads)
	}
	for _, test := range []struct {
		name string
		body string
		size uint64
	}{
		{name: "short", body: "a", size: 2},
		{name: "long", body: "ab", size: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			if reference, _, err := created.Put(context.Background(), asset.PutRequest{Body: bytes.NewBufferString(test.body), Size: test.size}); err == nil || reference.ID != "" {
				t.Fatalf("reference=%#v error=%v", reference, err)
			}
		})
	}
	if reference, info, err := created.Put(context.Background(), asset.PutRequest{Body: bytes.NewReader(nil), Size: 0}); err != nil || reference.ID == "" || info.Size != 0 {
		t.Fatalf("zero asset reference=%#v info=%#v error=%v", reference, info, err)
	}
}

func TestCapacityDedupAndIndependentReaders(t *testing.T) {
	created := newStore(t, t.TempDir(), Config{MaxObjectBytes: 4, MaxTotalBytes: 4})
	reference, _, err := created.Put(context.Background(), asset.PutRequest{Body: bytes.NewBufferString("data"), Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, _, err := created.Put(context.Background(), asset.PutRequest{Body: bytes.NewBufferString("data"), Size: 4})
	if err != nil || duplicate != reference {
		t.Fatalf("duplicate=%#v error=%v", duplicate, err)
	}
	if _, _, err := created.Put(context.Background(), asset.PutRequest{Body: bytes.NewBufferString("x"), Size: 1}); !errors.Is(err, ErrCapacity) {
		t.Fatalf("capacity error=%v", err)
	}
	first, err := created.Open(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	second, err := created.Open(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	defer second.Close()
	left, _ := io.ReadAll(first)
	right, _ := io.ReadAll(second)
	if !bytes.Equal(left, right) || string(left) != "data" {
		t.Fatalf("reads=(%q,%q)", left, right)
	}
}

func TestDifferentBytesHaveDifferentIDsAcrossStores(t *testing.T) {
	first := newStore(t, t.TempDir(), Config{})
	second := newStore(t, t.TempDir(), Config{})
	left, _, err := first.Put(context.Background(), asset.PutRequest{Body: bytes.NewBufferString("left"), Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	right, _, err := second.Put(context.Background(), asset.PutRequest{Body: bytes.NewBufferString("right"), Size: 5})
	if err != nil {
		t.Fatal(err)
	}
	if left.ID == right.ID {
		t.Fatalf("different bytes shared reference %q", left.ID)
	}
}

func TestCanceledWaitDoesNotReadPutBody(t *testing.T) {
	created := newStore(t, t.TempDir(), Config{IOConcurrency: 1})
	reference, _, err := created.Put(context.Background(), asset.PutRequest{Body: bytes.NewBufferString("held"), Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	held, err := created.Open(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader := &countingReader{Reader: bytes.NewBufferString("new")}
	if _, _, err := created.Put(ctx, asset.PutRequest{Body: reader, Size: 3}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if reader.reads != 0 {
		t.Fatalf("canceled put read body %d times", reader.reads)
	}
}

func TestStartupCleansIncompleteStagingAndConcurrentOpen(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, "staging")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "incomplete"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	created := newStore(t, root, Config{IOConcurrency: 32})
	if entries, err := os.ReadDir(staging); err != nil || len(entries) != 0 {
		t.Fatalf("staging entries=%v error=%v", entries, err)
	}
	reference, _, err := created.Put(context.Background(), asset.PutRequest{Body: bytes.NewBufferString("data"), Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for range 20 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			body, openErr := created.Open(context.Background(), reference)
			if openErr != nil {
				t.Errorf("Open: %v", openErr)
				return
			}
			defer body.Close()
			if raw, readErr := io.ReadAll(body); readErr != nil || string(raw) != "data" {
				t.Errorf("read=%q error=%v", raw, readErr)
			}
		}()
	}
	wait.Wait()
}

func TestInvalidAndMissingReferences(t *testing.T) {
	created := newStore(t, t.TempDir(), Config{})
	if _, err := created.Stat(context.Background(), asset.Reference{}); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("zero reference error=%v", err)
	}
	missing := asset.Reference{ID: referencePrefix + string(bytes.Repeat([]byte{'a'}, 64))}
	if _, err := created.Open(context.Background(), missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing reference error=%v", err)
	}
}

func TestNewRejectsInvalidStateDirectory(t *testing.T) {
	for _, directory := range []string{"", "relative"} {
		if _, _, err := New(context.Background(), Config{}, Dependencies{State: testScope(directory)}); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("directory %q error=%v", directory, err)
		}
	}
}

func TestPutRejectsCorruptExistingBlob(t *testing.T) {
	created := newStore(t, t.TempDir(), Config{})
	reference, _, err := created.Put(context.Background(), asset.PutRequest{Body: bytes.NewBufferString("first"), Size: 5})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(created.path(reference), []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := created.Put(context.Background(), asset.PutRequest{Body: bytes.NewBufferString("first"), Size: 5}); err == nil {
		t.Fatal("deduplication accepted a corrupt blob")
	}
}

type countingReader struct {
	io.Reader
	reads int
}

func (r *countingReader) Read(buffer []byte) (int, error) {
	r.reads++
	return r.Reader.Read(buffer)
}

package sessionjsonl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ingot-agent/sdk"
	"github.com/ingot-agent/sdk/config"
	"github.com/ingot-agent/sdk/session"
)

func newTestStore(t *testing.T) (*store, string) {
	t.Helper()
	root := t.TempDir()
	ctx := config.WithStateDir(context.Background(), root)
	exports, cleanup, err := New(ctx, Config{Durability: "sync"}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		t.Cleanup(func() { _ = cleanup(context.Background()) })
	}
	return exports.Store.(*store), root
}

func TestComponentContract(t *testing.T) {
	t.Parallel()
	var constructor func(context.Context, Config, Dependencies) (Exports, sdk.Cleanup, error) = New
	_ = constructor
	created, _ := newTestStore(t)
	var _ session.Store = created
	var _ session.MutableStore = created
}

func TestRequiresStateScope(t *testing.T) {
	t.Parallel()
	_, _, err := New(context.Background(), Config{}, Dependencies{})
	if !errors.Is(err, config.ErrStateDirUnavailable) {
		t.Fatalf("error = %v, want ErrStateDirUnavailable", err)
	}
}

func TestStateDirOwnerLock(t *testing.T) {
	root := t.TempDir()
	ctx := config.WithStateDir(context.Background(), root)
	first, firstCleanup, err := New(ctx, Config{}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Store == nil || firstCleanup == nil {
		t.Fatal("first store did not return store and cleanup")
	}
	defer func() { _ = firstCleanup(context.Background()) }()
	if _, _, err := New(ctx, Config{}, Dependencies{}); !errors.Is(err, ErrStateDirLocked) {
		t.Fatalf("second writer error = %v, want ErrStateDirLocked", err)
	}
}

func TestCreateAppendLoadAndOwnership(t *testing.T) {
	t.Parallel()
	created, _ := newTestStore(t)
	createdAt := time.Date(2026, 8, 22, 12, 0, 0, 0, time.FixedZone("test", 8*60*60))
	id, err := created.Create(context.Background(), session.Metadata{Title: "test", CreatedAt: createdAt})
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"message":"hello"}`)
	if err := created.Append(context.Background(), id, session.Entry{Kind: "message", Version: 1, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	payload[0] = '['
	entries, err := created.Load(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || string(entries[0].Payload) != `{"message":"hello"}` {
		t.Fatalf("entries = %#v", entries)
	}
	entries[0].Payload[0] = '['
	again, err := created.Load(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if string(again[0].Payload) != `{"message":"hello"}` {
		t.Fatalf("second load payload = %s", again[0].Payload)
	}
}

func TestRenamePersistsTitleWithoutChangingConversationTimes(t *testing.T) {
	t.Parallel()
	created, _ := newTestStore(t)
	createdAt := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	id, err := created.Create(context.Background(), session.Metadata{Title: "First message", CreatedAt: createdAt})
	if err != nil {
		t.Fatal(err)
	}
	appendedAt := createdAt.Add(time.Minute)
	created.now = func() time.Time { return appendedAt }
	if err := created.Append(context.Background(), id, session.Entry{Kind: "message", Version: 1, Payload: json.RawMessage(`"hello"`)}); err != nil {
		t.Fatal(err)
	}
	if err := created.Rename(context.Background(), id, "Generated title"); err != nil {
		t.Fatal(err)
	}
	summaries, err := created.List(context.Background(), session.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].Title != "Generated title" || !summaries[0].CreatedAt.Equal(createdAt) || !summaries[0].UpdatedAt.Equal(appendedAt) {
		t.Fatalf("summary=%#v", summaries)
	}
	entries, err := created.Load(context.Background(), id)
	if err != nil || len(entries) != 1 || string(entries[0].Payload) != `"hello"` {
		t.Fatalf("entries=%#v err=%v", entries, err)
	}
}

func TestRenameValidationAndContext(t *testing.T) {
	t.Parallel()
	created, _ := newTestStore(t)
	id, err := created.Create(context.Background(), session.Metadata{Title: "original", CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"", "   ", string([]byte{0xff})} {
		if err := created.Rename(context.Background(), id, title); !errors.Is(err, ErrInvalidEntry) {
			t.Fatalf("Rename(%q) error=%v", title, err)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := created.Rename(canceled, id, "new"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Rename error=%v", err)
	}
	missing := session.ID("0123456789abcdef0123456789abcdef")
	if err := created.Rename(context.Background(), missing, "new"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("missing Rename error=%v", err)
	}
}

func TestConcurrentAppendProducesCompleteSequence(t *testing.T) {
	t.Parallel()
	created, _ := newTestStore(t)
	id, err := created.Create(context.Background(), session.Metadata{Title: "parallel", CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	const count = 40
	var wait sync.WaitGroup
	for i := 0; i < count; i++ {
		wait.Add(1)
		go func(value int) {
			defer wait.Done()
			payload, _ := json.Marshal(map[string]int{"value": value})
			if appendErr := created.Append(context.Background(), id, session.Entry{Kind: "value", Version: 1, Payload: payload}); appendErr != nil {
				t.Errorf("Append(%d): %v", value, appendErr)
			}
		}(i)
	}
	wait.Wait()
	entries, err := created.Load(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != count {
		t.Fatalf("entries = %d, want %d", len(entries), count)
	}
	seen := make(map[int]bool, count)
	for _, entry := range entries {
		var value map[string]int
		if err := json.Unmarshal(entry.Payload, &value); err != nil {
			t.Fatal(err)
		}
		seen[value["value"]] = true
	}
	if len(seen) != count {
		t.Fatalf("unique values = %d, want %d", len(seen), count)
	}
}

func TestConcurrentRenameAndAppendShareSessionOrdering(t *testing.T) {
	t.Parallel()
	created, _ := newTestStore(t)
	id, err := created.Create(context.Background(), session.Metadata{Title: "initial", CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	const count = 20
	var wait sync.WaitGroup
	for i := 0; i < count; i++ {
		wait.Add(2)
		go func(value int) {
			defer wait.Done()
			payload, _ := json.Marshal(value)
			if err := created.Append(context.Background(), id, session.Entry{Kind: "value", Version: 1, Payload: payload}); err != nil {
				t.Errorf("Append(%d): %v", value, err)
			}
		}(i)
		go func(value int) {
			defer wait.Done()
			if err := created.Rename(context.Background(), id, fmt.Sprintf("title-%d", value)); err != nil {
				t.Errorf("Rename(%d): %v", value, err)
			}
		}(i)
	}
	wait.Wait()
	entries, err := created.Load(context.Background(), id)
	if err != nil || len(entries) != count {
		t.Fatalf("entries=%d err=%v", len(entries), err)
	}
	summaries, err := created.List(context.Background(), session.Query{})
	if err != nil || len(summaries) != 1 || !strings.HasPrefix(summaries[0].Title, "title-") {
		t.Fatalf("summaries=%#v err=%v", summaries, err)
	}
}

func TestGateWaitObservesContext(t *testing.T) {
	t.Parallel()
	manager := newGateManager()
	release, err := manager.acquire(context.Background(), "same")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.acquire(ctx, "same"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestCreateWaitObservesContext(t *testing.T) {
	created, _ := newTestStore(t)
	release, err := created.acquireCreate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = created.Create(ctx, session.Metadata{Title: "cancelled", CreatedAt: time.Now()})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Create error = %v, want context.Canceled", err)
	}
}

func TestTailRecoveryAndMiddleCorruption(t *testing.T) {
	t.Parallel()
	created, _ := newTestStore(t)
	id, err := created.Create(context.Background(), session.Metadata{Title: "recovery", CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := created.Append(context.Background(), id, session.Entry{Kind: "ok", Version: 1, Payload: json.RawMessage(`true`)}); err != nil {
		t.Fatal(err)
	}
	path := created.entriesPath(id)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString(`{"partial":`)
	_ = file.Close()
	entries, err := created.Load(context.Background(), id)
	if err != nil || len(entries) != 1 {
		t.Fatalf("Load after tail = %d, %v", len(entries), err)
	}
	if err := os.WriteFile(path, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := created.Load(context.Background(), id); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("error = %v, want ErrCorruptState", err)
	}
}

func TestListOrderingAndPagination(t *testing.T) {
	created, _ := newTestStore(t)
	base := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	first, err := created.Create(context.Background(), session.Metadata{Title: "first", CreatedAt: base})
	if err != nil {
		t.Fatal(err)
	}
	second, err := created.Create(context.Background(), session.Metadata{Title: "second", CreatedAt: base.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	created.now = func() time.Time { return base.Add(2 * time.Minute) }
	if err := created.Append(context.Background(), first, session.Entry{Kind: "message", Version: 1, Payload: json.RawMessage(`null`)}); err != nil {
		t.Fatal(err)
	}
	summaries, err := created.List(context.Background(), session.Query{})
	if err != nil {
		t.Fatal(err)
	}
	got := []session.ID{summaries[0].ID, summaries[1].ID}
	want := []session.ID{first, second}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %#v, want %#v", got, want)
	}
	page, err := created.List(context.Background(), session.Query{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].ID != second {
		t.Fatalf("page = %#v", page)
	}
}

func TestNotFoundInvalidIDAndQuery(t *testing.T) {
	t.Parallel()
	created, _ := newTestStore(t)
	if _, err := created.Load(context.Background(), session.ID("bad")); !errors.Is(err, ErrInvalidSessionID) {
		t.Fatalf("invalid id error = %v", err)
	}
	if _, err := created.Load(context.Background(), session.ID("0123456789abcdef0123456789abcdef")); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("not found error = %v", err)
	}
	if _, err := created.List(context.Background(), session.Query{Limit: -1}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("query error = %v", err)
	}
}

func TestUnsupportedStateVersion(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "state.json"), []byte(`{"schema_version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := config.WithStateDir(context.Background(), root)
	_, _, err := New(ctx, Config{}, Dependencies{})
	if !errors.Is(err, ErrUnsupportedState) {
		t.Fatalf("error = %v, want ErrUnsupportedState", err)
	}
}

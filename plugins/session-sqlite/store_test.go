package sessionsqlite

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ingot-agent/sdk/session"
)

func TestPersistenceLifecycleForkAndDelete(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "sessions.sqlite3")
	created, err := openStore(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = created.close() })

	base := time.Date(2026, time.September, 2, 10, 0, 0, 0, time.UTC)
	now := base
	created.now = func() time.Time { return now }
	ids := []session.ID{"source", "target", "second"}
	created.generateID = func() (session.ID, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	}

	source, err := created.Create(ctx, session.CreateRequest{Title: "Original"})
	if err != nil {
		t.Fatal(err)
	}
	if source.ID != "source" || source.Title != "Original" || !source.CreatedAt.Equal(base) || !source.UpdatedAt.Equal(base) || source.ArchivedAt != nil {
		t.Fatalf("created metadata=%#v", source)
	}

	now = base.Add(time.Minute)
	payload := []byte{0, 1, 2, 0xff}
	if err := created.Append(ctx, source.ID, session.Entry{Kind: "opaque", Version: 7, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	payload[0] = 9
	afterAppend, err := created.Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !afterAppend.UpdatedAt.Equal(now) {
		t.Fatalf("UpdatedAt=%v want=%v", afterAppend.UpdatedAt, now)
	}
	entries, err := created.Load(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantEntries := []session.Entry{{Kind: "opaque", Version: 7, Payload: []byte{0, 1, 2, 0xff}}}
	if !reflect.DeepEqual(entries, wantEntries) {
		t.Fatalf("entries=%#v want=%#v", entries, wantEntries)
	}
	entries[0].Payload[0] = 8
	again, err := created.Load(ctx, source.ID)
	if err != nil || !bytes.Equal(again[0].Payload, wantEntries[0].Payload) {
		t.Fatalf("caller mutated durable payload: entries=%#v err=%v", again, err)
	}

	now = base.Add(2 * time.Minute)
	renamed, err := created.Rename(ctx, source.ID, "Renamed")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Title != "Renamed" || !renamed.UpdatedAt.Equal(afterAppend.UpdatedAt) {
		t.Fatalf("renamed metadata=%#v", renamed)
	}
	archived, err := created.Archive(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if archived.ArchivedAt == nil || !archived.ArchivedAt.Equal(now) || !archived.UpdatedAt.Equal(afterAppend.UpdatedAt) {
		t.Fatalf("archived metadata=%#v", archived)
	}
	now = base.Add(3 * time.Minute)
	archivedAgain, err := created.Archive(ctx, source.ID)
	if err != nil || archivedAgain.ArchivedAt == nil || !archivedAgain.ArchivedAt.Equal(*archived.ArchivedAt) {
		t.Fatalf("idempotent archive=%#v err=%v", archivedAgain, err)
	}
	if err := created.Append(ctx, source.ID, session.Entry{Kind: "late"}); !errors.Is(err, session.ErrArchived) {
		t.Fatalf("archived Append error=%v", err)
	}
	if _, err := created.Load(ctx, source.ID); err != nil {
		t.Fatalf("archived Load error=%v", err)
	}

	target, err := created.Fork(ctx, source.ID, session.ForkRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if target.ID != "target" || target.Title != "Renamed" || target.ArchivedAt != nil || !target.CreatedAt.Equal(now) || !target.UpdatedAt.Equal(now) {
		t.Fatalf("fork target=%#v", target)
	}
	targetEntries, err := created.Load(ctx, target.ID)
	if err != nil || !reflect.DeepEqual(targetEntries, wantEntries) {
		t.Fatalf("fork entries=%#v err=%v", targetEntries, err)
	}

	restored, err := created.Restore(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.ArchivedAt != nil || !restored.UpdatedAt.Equal(afterAppend.UpdatedAt) {
		t.Fatalf("restored metadata=%#v", restored)
	}
	restoredAgain, err := created.Restore(ctx, source.ID)
	if err != nil || restoredAgain.ArchivedAt != nil {
		t.Fatalf("idempotent restore=%#v err=%v", restoredAgain, err)
	}

	if err := created.Delete(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	assertNotFoundAfterDelete(t, created, source.ID)
	var sourceEntries int
	if err := created.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM entries WHERE session_id = ?", string(source.ID)).Scan(&sourceEntries); err != nil {
		t.Fatal(err)
	}
	if sourceEntries != 0 {
		t.Fatalf("deleted source retained %d entries", sourceEntries)
	}
}

func TestListOrdersBothLifecycleStatesDeterministically(t *testing.T) {
	ctx := context.Background()
	created, err := openStore(ctx, filepath.Join(t.TempDir(), "sessions.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = created.close() })

	base := time.Date(2026, time.September, 2, 11, 0, 0, 0, time.UTC)
	now := base
	created.now = func() time.Time { return now }
	ids := []session.ID{"first", "second"}
	created.generateID = func() (session.ID, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	}
	first, err := created.Create(ctx, session.CreateRequest{Title: "First"})
	if err != nil {
		t.Fatal(err)
	}
	now = base.Add(time.Minute)
	second, err := created.Create(ctx, session.CreateRequest{Title: "Second"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := created.Archive(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	now = base.Add(2 * time.Minute)
	if err := created.Append(ctx, first.ID, session.Entry{Kind: "activity", Payload: []byte("x")}); err != nil {
		t.Fatal(err)
	}

	listed, err := created.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := []session.ID{listed[0].ID, listed[1].ID}, []session.ID{first.ID, second.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("List order=%v want=%v", got, want)
	}
	if listed[1].ArchivedAt == nil {
		t.Fatalf("List omitted archived state: %#v", listed[1])
	}
}

func TestConcurrentAppendFormsOneCompleteOrder(t *testing.T) {
	ctx := context.Background()
	created, err := openStore(ctx, filepath.Join(t.TempDir(), "sessions.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = created.close() })
	metadata, err := created.Create(ctx, session.CreateRequest{Title: "Concurrent"})
	if err != nil {
		t.Fatal(err)
	}

	const count = 32
	errorsByIndex := make([]error, count)
	var wait sync.WaitGroup
	for index := 0; index < count; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			errorsByIndex[index] = created.Append(ctx, metadata.ID, session.Entry{Kind: "value", Version: 1, Payload: []byte{byte(index)}})
		}(index)
	}
	wait.Wait()
	for index, err := range errorsByIndex {
		if err != nil {
			t.Fatalf("Append %d error=%v", index, err)
		}
	}
	entries, err := created.Load(ctx, metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != count {
		t.Fatalf("entry count=%d want=%d", len(entries), count)
	}
	seen := make(map[byte]bool, count)
	for _, entry := range entries {
		if len(entry.Payload) != 1 || seen[entry.Payload[0]] {
			t.Fatalf("invalid or duplicate entry=%#v", entry)
		}
		seen[entry.Payload[0]] = true
	}
}

func TestDatabasePersistsAcrossOpen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sessions.sqlite3")
	first, err := openStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := first.Create(ctx, session.CreateRequest{Title: "Persistent"})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Append(ctx, metadata.ID, session.Entry{Kind: "binary", Payload: []byte{0, 1, 0}}); err != nil {
		t.Fatal(err)
	}
	if err := first.close(); err != nil {
		t.Fatal(err)
	}
	second, err := openStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.close() })
	got, err := second.Get(ctx, metadata.ID)
	if err != nil || got.Title != "Persistent" {
		t.Fatalf("Get after reopen=%#v err=%v", got, err)
	}
	entries, err := second.Load(ctx, metadata.ID)
	if err != nil || len(entries) != 1 || !bytes.Equal(entries[0].Payload, []byte{0, 1, 0}) {
		t.Fatalf("Load after reopen=%#v err=%v", entries, err)
	}
}

func TestCanceledContextAndMissingSessionErrors(t *testing.T) {
	created, err := openStore(context.Background(), filepath.Join(t.TempDir(), "sessions.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = created.close() })
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := created.Create(canceled, session.CreateRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Create error=%v", err)
	}
	if _, err := created.List(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("List error=%v", err)
	}
	if _, err := created.Get(context.Background(), "missing"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("Get missing error=%v", err)
	}
	if err := created.Delete(context.Background(), "missing"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("Delete missing error=%v", err)
	}
}

func assertNotFoundAfterDelete(t *testing.T, created *store, id session.ID) {
	t.Helper()
	ctx := context.Background()
	checks := []struct {
		name string
		run  func() error
	}{
		{name: "Get", run: func() error { _, err := created.Get(ctx, id); return err }},
		{name: "Load", run: func() error { _, err := created.Load(ctx, id); return err }},
		{name: "Append", run: func() error { return created.Append(ctx, id, session.Entry{}) }},
		{name: "Rename", run: func() error { _, err := created.Rename(ctx, id, "x"); return err }},
		{name: "Archive", run: func() error { _, err := created.Archive(ctx, id); return err }},
		{name: "Restore", run: func() error { _, err := created.Restore(ctx, id); return err }},
		{name: "Fork", run: func() error { _, err := created.Fork(ctx, id, session.ForkRequest{}); return err }},
		{name: "Delete", run: func() error { return created.Delete(ctx, id) }},
	}
	for _, check := range checks {
		if err := check.run(); !errors.Is(err, session.ErrNotFound) {
			t.Fatalf("%s after Delete error=%v", check.name, err)
		}
	}
}

package sessionjsonl

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/session"
)

const (
	stateSchemaVersion = 1
	recordVersion      = 1
	maxIDAttempts      = 10
)

type stateFile struct {
	SchemaVersion int `json:"schema_version"`
}

type metadataFile struct {
	RecordVersion int       `json:"record_version"`
	ID            string    `json:"id"`
	Title         string    `json:"title"`
	CreatedAt     time.Time `json:"created_at"`
}

type persistedEntry struct {
	Kind    string `json:"kind"`
	Version int    `json:"version"`
	Payload []byte `json:"payload"`
}

type entryRecord struct {
	RecordVersion int            `json:"record_version"`
	AppendedAt    time.Time      `json:"appended_at"`
	Entry         persistedEntry `json:"entry"`
}

type store struct {
	root       string
	sessions   string
	syncWrites bool
	gates      *gateManager
	createGate chan struct{}
	now        func() time.Time
	random     io.Reader
}

func openStore(ctx context.Context, root string, syncWrites bool) (*store, func() error, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve session state directory: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, nil, fmt.Errorf("create session state directory: %w", err)
	}
	release, err := acquireOwnerLock(ownerLockPath(absolute))
	if err != nil {
		return nil, nil, fmt.Errorf("acquire session state owner lock: %w", err)
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = release()
		}
	}()
	created := &store{
		root:       filepath.Clean(absolute),
		sessions:   filepath.Join(filepath.Clean(absolute), "sessions"),
		syncWrites: syncWrites,
		gates:      newGateManager(),
		createGate: make(chan struct{}, 1),
		now:        time.Now,
		random:     rand.Reader,
	}
	created.createGate <- struct{}{}
	if err := created.initializeState(ctx); err != nil {
		return nil, nil, err
	}
	closeOnError = false
	return created, release, nil
}

func (s *store) initializeState(ctx context.Context) error {
	statePath := filepath.Join(s.root, "state.json")
	data, err := os.ReadFile(statePath)
	if errors.Is(err, fs.ErrNotExist) {
		entries, readErr := os.ReadDir(s.root)
		if readErr != nil {
			return fmt.Errorf("inspect empty session state: %w", readErr)
		}
		for _, entry := range entries {
			if entry.Name() == ownerLockFileName {
				continue
			}
			return fmt.Errorf("state.json is missing from non-empty state directory: %w", ErrCorruptState)
		}
		if err := writeNewJSON(statePath, stateFile{SchemaVersion: stateSchemaVersion}, s.syncWrites); err != nil {
			return fmt.Errorf("initialize session state: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("read session state version: %w", err)
	} else {
		var state stateFile
		if err := exactJSON(data, &state); err != nil {
			return fmt.Errorf("decode state.json: %w: %w", ErrCorruptState, err)
		}
		if state.SchemaVersion != stateSchemaVersion {
			return fmt.Errorf("state schema %d, reader window [1,1]: %w", state.SchemaVersion, ErrUnsupportedState)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.sessions, 0o700); err != nil {
		return fmt.Errorf("create sessions directory: %w", err)
	}
	entries, err := os.ReadDir(s.sessions)
	if err != nil {
		return fmt.Errorf("inspect session candidates: %w", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), sessionCandidatePrefix) {
			if err := os.RemoveAll(filepath.Join(s.sessions, entry.Name())); err != nil {
				return fmt.Errorf("remove stale session candidate %q: %w", entry.Name(), err)
			}
		}
	}
	return nil
}

func (s *store) Create(ctx context.Context, metadata session.Metadata) (session.ID, error) {
	if err := checkStoreContext(ctx); err != nil {
		return "", err
	}
	if metadata.CreatedAt.IsZero() || !utf8.ValidString(metadata.Title) {
		return "", fmt.Errorf("metadata requires valid UTF-8 title and non-zero created_at: %w", ErrInvalidEntry)
	}

	releaseCreate, err := s.acquireCreate(ctx)
	if err != nil {
		return "", err
	}
	defer releaseCreate()
	for attempt := 0; attempt < maxIDAttempts; attempt++ {
		if err := checkStoreContext(ctx); err != nil {
			return "", err
		}
		id, err := s.generateID()
		if err != nil {
			return "", fmt.Errorf("generate session id: %w", err)
		}
		directory := s.sessionDirectory(id)
		if _, err := os.Lstat(directory); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("create session %s: %w", id, err)
		}
		candidate := filepath.Join(s.sessions, sessionCandidatePrefix+string(id))
		if err := os.Mkdir(candidate, 0o700); errors.Is(err, fs.ErrExist) {
			continue
		} else if err != nil {
			return "", fmt.Errorf("create session %s candidate: %w", id, err)
		}

		published := false
		defer func() {
			if !published {
				removeCandidate(candidate)
			}
		}()
		diskMetadata := metadataFile{
			RecordVersion: recordVersion,
			ID:            string(id),
			Title:         metadata.Title,
			CreatedAt:     metadata.CreatedAt.UTC(),
		}
		if err := writeNewJSON(filepath.Join(candidate, "metadata.json"), diskMetadata, s.syncWrites); err != nil {
			return "", fmt.Errorf("write session %s metadata: %w", id, err)
		}
		entries, err := os.OpenFile(filepath.Join(candidate, "entries.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return "", fmt.Errorf("create session %s entries: %w", id, err)
		}
		if s.syncWrites {
			err = entries.Sync()
		}
		closeErr := entries.Close()
		if err != nil {
			return "", fmt.Errorf("sync session %s entries: %w", id, err)
		}
		if closeErr != nil {
			return "", fmt.Errorf("close session %s entries: %w", id, closeErr)
		}
		if s.syncWrites {
			if err := syncDirectory(candidate); err != nil {
				return "", fmt.Errorf("sync session %s directory: %w", id, err)
			}
		}
		if err := checkStoreContext(ctx); err != nil {
			return "", err
		}
		if err := os.Rename(candidate, directory); err != nil {
			if errors.Is(err, fs.ErrExist) {
				removeCandidate(candidate)
				continue
			}
			return "", fmt.Errorf("publish session %s: %w", id, err)
		}
		if s.syncWrites {
			if err := syncDirectory(s.sessions); err != nil {
				return "", fmt.Errorf("sync sessions directory: %w", err)
			}
		}
		published = true
		return id, nil
	}
	return "", errors.New("generate unique session id: retry limit reached")
}

func (s *store) Append(ctx context.Context, id session.ID, entry session.Entry) error {
	if err := validateSessionID(id); err != nil {
		return err
	}
	if err := validateEntry(entry); err != nil {
		return err
	}
	release, err := s.gates.acquire(ctx, string(id))
	if err != nil {
		return err
	}
	defer release()
	if err := checkStoreContext(ctx); err != nil {
		return err
	}

	if _, err := s.loadMetadata(id); err != nil {
		return err
	}
	entriesPath := s.entriesPath(id)
	if _, _, err := s.readRecords(ctx, entriesPath, true); err != nil {
		return err
	}
	payload := append([]byte(nil), entry.Payload...)
	record := entryRecord{
		RecordVersion: recordVersion,
		AppendedAt:    s.now().UTC(),
		Entry: persistedEntry{
			Kind: entry.Kind, Version: entry.Version, Payload: payload,
		},
	}
	line, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode session %s entry: %w", id, err)
	}
	line = append(line, '\n')
	if err := checkStoreContext(ctx); err != nil {
		return err
	}
	file, err := os.OpenFile(entriesPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return fmt.Errorf("open session %s entries: %w", id, err)
	}
	if contextErr := checkStoreContext(ctx); contextErr != nil {
		return errors.Join(contextErr, file.Close())
	}
	writeErr := writeFull(file, line)
	if writeErr == nil && s.syncWrites {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("append session %s entry: %w: %w", id, ErrCommitUnknown, writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close session %s entry: %w: %w", id, ErrCommitUnknown, closeErr)
	}
	return nil
}

func (s *store) Load(ctx context.Context, id session.ID) ([]session.Entry, error) {
	if err := validateSessionID(id); err != nil {
		return nil, err
	}
	release, err := s.gates.acquire(ctx, string(id))
	if err != nil {
		return nil, err
	}
	defer release()
	if err := checkStoreContext(ctx); err != nil {
		return nil, err
	}
	if _, err := s.loadMetadata(id); err != nil {
		return nil, err
	}
	entries, _, err := s.readRecords(ctx, s.entriesPath(id), true)
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func (s *store) Rename(ctx context.Context, id session.ID, title string) error {
	if err := validateSessionID(id); err != nil {
		return err
	}
	if title == "" || strings.TrimSpace(title) == "" || !utf8.ValidString(title) {
		return fmt.Errorf("title must be valid UTF-8 and non-empty: %w", ErrInvalidEntry)
	}
	release, err := s.gates.acquire(ctx, string(id))
	if err != nil {
		return err
	}
	defer release()
	if err := checkStoreContext(ctx); err != nil {
		return err
	}

	metadata, err := s.loadMetadata(id)
	if err != nil {
		return err
	}
	metadata.Title = title
	if err := replaceJSON(filepath.Join(s.sessionDirectory(id), "metadata.json"), metadata, s.syncWrites); err != nil {
		return fmt.Errorf("rename session %s: %w", id, err)
	}
	return nil
}

func (s *store) List(ctx context.Context, query session.Query) ([]session.Summary, error) {
	if query.Limit < 0 || query.Offset < 0 {
		return nil, fmt.Errorf("limit and offset must be non-negative: %w", ErrInvalidQuery)
	}
	if err := checkStoreContext(ctx); err != nil {
		return nil, err
	}
	directories, err := os.ReadDir(s.sessions)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	summaries := make([]session.Summary, 0, len(directories))
	for _, directory := range directories {
		if err := checkStoreContext(ctx); err != nil {
			return nil, err
		}
		id := session.ID(directory.Name())
		if strings.HasPrefix(directory.Name(), sessionCandidatePrefix) {
			continue
		}
		if !directory.IsDir() || validateSessionID(id) != nil {
			return nil, fmt.Errorf("unexpected entry %q in sessions directory: %w", directory.Name(), ErrCorruptState)
		}
		release, err := s.gates.acquire(ctx, string(id))
		if err != nil {
			return nil, err
		}
		if contextErr := checkStoreContext(ctx); contextErr != nil {
			release()
			return nil, contextErr
		}
		metadata, metadataErr := s.loadMetadata(id)
		if metadataErr != nil {
			release()
			return nil, metadataErr
		}
		_, lastAppend, recordsErr := s.readRecords(ctx, s.entriesPath(id), true)
		release()
		if recordsErr != nil {
			return nil, recordsErr
		}
		updatedAt := metadata.CreatedAt
		if !lastAppend.IsZero() {
			updatedAt = lastAppend
		}
		summaries = append(summaries, session.Summary{
			ID: id, Title: metadata.Title, CreatedAt: metadata.CreatedAt, UpdatedAt: updatedAt,
		})
	}
	sort.Slice(summaries, func(i, j int) bool {
		if !summaries[i].UpdatedAt.Equal(summaries[j].UpdatedAt) {
			return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt)
		}
		if !summaries[i].CreatedAt.Equal(summaries[j].CreatedAt) {
			return summaries[i].CreatedAt.After(summaries[j].CreatedAt)
		}
		return summaries[i].ID < summaries[j].ID
	})
	if query.Offset >= len(summaries) {
		return []session.Summary{}, nil
	}
	end := len(summaries)
	if query.Limit > 0 && query.Limit < end-query.Offset {
		end = query.Offset + query.Limit
	}
	result := make([]session.Summary, end-query.Offset)
	copy(result, summaries[query.Offset:end])
	return result, nil
}

func (s *store) readRecords(ctx context.Context, path string, recoverTail bool) ([]session.Entry, time.Time, error) {
	if err := checkStoreContext(ctx); err != nil {
		return nil, time.Time{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		if !recoverTail {
			return nil, time.Time{}, fmt.Errorf("incomplete final JSONL record: %w", ErrCorruptState)
		}
		lastNewline := bytes.LastIndexByte(data, '\n')
		validLength := int64(lastNewline + 1)
		if err := os.Truncate(path, validLength); err != nil {
			return nil, time.Time{}, fmt.Errorf("truncate incomplete JSONL tail: %w", err)
		}
		if s.syncWrites {
			file, openErr := os.OpenFile(path, os.O_WRONLY, 0)
			if openErr != nil {
				return nil, time.Time{}, openErr
			}
			syncErr := file.Sync()
			closeErr := file.Close()
			if syncErr != nil {
				return nil, time.Time{}, syncErr
			}
			if closeErr != nil {
				return nil, time.Time{}, closeErr
			}
		}
		data = data[:validLength]
	}
	entries := []session.Entry{}
	var lastAppend time.Time
	lines := bytes.Split(data, []byte{'\n'})
	for index, line := range lines {
		if len(line) == 0 && index == len(lines)-1 {
			continue
		}
		if err := checkStoreContext(ctx); err != nil {
			return nil, time.Time{}, err
		}
		if len(line) == 0 {
			return nil, time.Time{}, fmt.Errorf("empty JSONL line %d: %w", index+1, ErrCorruptState)
		}
		var record entryRecord
		if err := exactJSON(line, &record); err != nil {
			return nil, time.Time{}, fmt.Errorf("decode JSONL line %d: %w: %w", index+1, ErrCorruptState, err)
		}
		if record.RecordVersion != recordVersion || record.AppendedAt.IsZero() {
			return nil, time.Time{}, fmt.Errorf("invalid JSONL envelope at line %d: %w", index+1, ErrCorruptState)
		}
		entry := session.Entry{
			Kind: record.Entry.Kind, Version: record.Entry.Version,
			Payload: append([]byte(nil), record.Entry.Payload...),
		}
		if err := validateEntry(entry); err != nil {
			return nil, time.Time{}, fmt.Errorf("invalid JSONL entry at line %d: %w: %w", index+1, ErrCorruptState, err)
		}
		entries = append(entries, entry)
		lastAppend = record.AppendedAt.UTC()
	}
	return entries, lastAppend, nil
}

func (s *store) loadMetadata(id session.ID) (metadataFile, error) {
	data, err := os.ReadFile(filepath.Join(s.sessionDirectory(id), "metadata.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return metadataFile{}, fmt.Errorf("session %s: %w", id, session.ErrNotFound)
	}
	if err != nil {
		return metadataFile{}, fmt.Errorf("read session %s metadata: %w", id, err)
	}
	var metadata metadataFile
	if err := exactJSON(data, &metadata); err != nil {
		return metadataFile{}, fmt.Errorf("decode session %s metadata: %w: %w", id, ErrCorruptState, err)
	}
	if metadata.RecordVersion != recordVersion || metadata.ID != string(id) || metadata.CreatedAt.IsZero() || !utf8.ValidString(metadata.Title) {
		return metadataFile{}, fmt.Errorf("invalid session %s metadata: %w", id, ErrCorruptState)
	}
	metadata.CreatedAt = metadata.CreatedAt.UTC()
	return metadata, nil
}

func (s *store) generateID() (session.ID, error) {
	value := make([]byte, 16)
	if _, err := io.ReadFull(s.random, value); err != nil {
		return "", err
	}
	return session.ID(hex.EncodeToString(value)), nil
}

func (s *store) sessionDirectory(id session.ID) string {
	return filepath.Join(s.sessions, string(id))
}

func (s *store) entriesPath(id session.ID) string {
	return filepath.Join(s.sessionDirectory(id), "entries.jsonl")
}

func validateSessionID(id session.ID) error {
	if len(id) != 32 {
		return fmt.Errorf("session id %q: %w", id, ErrInvalidSessionID)
	}
	for _, character := range id {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return fmt.Errorf("session id %q: %w", id, ErrInvalidSessionID)
		}
	}
	return nil
}

func validateEntry(entry session.Entry) error {
	if entry.Kind == "" || !utf8.ValidString(entry.Kind) || entry.Version <= 0 {
		return fmt.Errorf("kind must be UTF-8 and non-empty and version positive: %w", ErrInvalidEntry)
	}
	return nil
}

func writeNewJSON(path string, value any, syncWrites bool) error {
	return writeJSON(path, value, syncWrites, false)
}

func replaceJSON(path string, value any, syncWrites bool) error {
	return writeJSON(path, value, syncWrites, true)
}

func writeJSON(path string, value any, syncWrites, replace bool) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".ingot-state-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if err := writeFull(temporary, data); err != nil {
		return err
	}
	if syncWrites {
		if err := temporary.Sync(); err != nil {
			return err
		}
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if !replace {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("destination already exists: %w", fs.ErrExist)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := atomicReplace(temporaryPath, path); err != nil {
		return err
	}
	committed = true
	if syncWrites {
		if err := syncDirectory(directory); err != nil {
			return err
		}
	}
	return nil
}

const sessionCandidatePrefix = ".ingot-session-"

func (s *store) acquireCreate(ctx context.Context) (func(), error) {
	if ctx == nil {
		return nil, context.Canceled
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.createGate:
	}
	var once sync.Once
	return func() {
		once.Do(func() { s.createGate <- struct{}{} })
	}, nil
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		count, err := writer.Write(data)
		data = data[count:]
		if err != nil {
			return err
		}
		if count == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func checkStoreContext(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	return ctx.Err()
}

var _ session.MutableStore = (*store)(nil)

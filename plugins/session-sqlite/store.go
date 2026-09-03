package sessionsqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/ingot-agent/sdk/session"
	_ "modernc.org/sqlite"
)

const schemaVersion = 1

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
    id          TEXT PRIMARY KEY,
    title       TEXT NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    archived_at INTEGER
);

CREATE TABLE IF NOT EXISTS entries (
    session_id TEXT NOT NULL,
    sequence   INTEGER NOT NULL,
    kind       TEXT NOT NULL,
    version    INTEGER NOT NULL,
    payload    BLOB NOT NULL,

    PRIMARY KEY (session_id, sequence),
    FOREIGN KEY (session_id)
        REFERENCES sessions(id)
        ON DELETE CASCADE
);
`

type store struct {
	db         *sql.DB
	now        func() time.Time
	generateID func() (session.ID, error)
}

func openStore(ctx context.Context, databasePath string) (*store, error) {
	if ctx == nil {
		return nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(databasePath) {
		return nil, fmt.Errorf("database path must be absolute: %w", ErrInvalidDependencies)
	}
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		return nil, fmt.Errorf("create session state directory: %w", err)
	}
	parameters := url.Values{}
	parameters.Add("_pragma", "busy_timeout(5000)")
	parameters.Add("_pragma", "foreign_keys(1)")
	dsn := (&url.URL{Scheme: "file", Path: databasePath, RawQuery: parameters.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open session database: %w", err)
	}
	// One connection gives every operation the same foreign-key configuration
	// and provides a strong, transaction-ordered Fork/Append/Delete boundary.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	created := &store{db: db, now: time.Now, generateID: randomID}
	if err := created.initialize(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(databasePath, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("restrict session database permissions: %w", err)
	}
	return created, nil
}

func (s *store) initialize(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect session database: %w", err)
	}
	var foreignKeys int
	if err := s.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("inspect foreign-key enforcement: %w", err)
	}
	if foreignKeys != 1 {
		return errors.New("session database foreign-key enforcement is disabled")
	}
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("inspect session schema version: %w", err)
	}
	if version != 0 && version != schemaVersion {
		return fmt.Errorf("schema version %d: %w", version, ErrUnsupportedSchema)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin session schema transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("initialize session schema: %w", err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("record session schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit session schema: %w", err)
	}
	return nil
}

func (s *store) close() error {
	return s.db.Close()
}

func (s *store) Create(ctx context.Context, request session.CreateRequest) (session.Metadata, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return session.Metadata{}, err
	}
	defer tx.Rollback()
	id, err := s.generateID()
	if err != nil {
		return session.Metadata{}, fmt.Errorf("generate session ID: %w", err)
	}
	now := s.now().UTC()
	metadata := session.Metadata{ID: id, Title: request.Title, CreatedAt: now, UpdatedAt: now}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO sessions (id, title, created_at, updated_at, archived_at) VALUES (?, ?, ?, ?, NULL)",
		string(id), request.Title, encodeTime(now), encodeTime(now),
	); err != nil {
		return session.Metadata{}, fmt.Errorf("create session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return session.Metadata{}, fmt.Errorf("commit session creation: %w", err)
	}
	return metadata, nil
}

func (s *store) Append(ctx context.Context, id session.ID, entry session.Entry) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	metadata, err := metadataByID(ctx, tx, id)
	if err != nil {
		return err
	}
	if metadata.ArchivedAt != nil {
		return fmt.Errorf("append session %q: %w", id, session.ErrArchived)
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(sequence), -1) + 1 FROM entries WHERE session_id = ?", string(id),
	).Scan(&sequence); err != nil {
		return fmt.Errorf("determine next entry sequence for session %q: %w", id, err)
	}
	payload := entry.Payload
	if payload == nil {
		payload = []byte{}
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO entries (session_id, sequence, kind, version, payload) VALUES (?, ?, ?, ?, ?)",
		string(id), sequence, entry.Kind, entry.Version, payload,
	); err != nil {
		return fmt.Errorf("append session %q: %w", id, err)
	}
	updatedAt := s.nextCommitTime(metadata.UpdatedAt)
	if _, err := tx.ExecContext(ctx, "UPDATE sessions SET updated_at = ? WHERE id = ?", encodeTime(updatedAt), string(id)); err != nil {
		return fmt.Errorf("update session %q activity: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit append to session %q: %w", id, err)
	}
	return nil
}

func (s *store) Load(ctx context.Context, id session.ID) ([]session.Entry, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := metadataByID(ctx, tx, id); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx,
		"SELECT kind, version, payload FROM entries WHERE session_id = ? ORDER BY sequence ASC", string(id),
	)
	if err != nil {
		return nil, fmt.Errorf("load session %q entries: %w", id, err)
	}
	entries := []session.Entry{}
	for rows.Next() {
		var entry session.Entry
		if err := rows.Scan(&entry.Kind, &entry.Version, &entry.Payload); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan session %q entry: %w", id, err)
		}
		entry.Payload = append([]byte(nil), entry.Payload...)
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate session %q entries: %w", id, err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close session %q entry rows: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("finish loading session %q: %w", id, err)
	}
	return entries, nil
}

func (s *store) Get(ctx context.Context, id session.ID) (session.Metadata, error) {
	if ctx == nil {
		return session.Metadata{}, context.Canceled
	}
	metadata, err := metadataByID(ctx, s.db, id)
	if err != nil {
		return session.Metadata{}, err
	}
	return metadata, nil
}

func (s *store) Rename(ctx context.Context, id session.ID, title string) (session.Metadata, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return session.Metadata{}, err
	}
	defer tx.Rollback()
	if _, err := metadataByID(ctx, tx, id); err != nil {
		return session.Metadata{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE sessions SET title = ? WHERE id = ?", title, string(id)); err != nil {
		return session.Metadata{}, fmt.Errorf("rename session %q: %w", id, err)
	}
	metadata, err := metadataByID(ctx, tx, id)
	if err != nil {
		return session.Metadata{}, err
	}
	if err := tx.Commit(); err != nil {
		return session.Metadata{}, fmt.Errorf("commit rename of session %q: %w", id, err)
	}
	return metadata, nil
}

func (s *store) Archive(ctx context.Context, id session.ID) (session.Metadata, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return session.Metadata{}, err
	}
	defer tx.Rollback()
	metadata, err := metadataByID(ctx, tx, id)
	if err != nil {
		return session.Metadata{}, err
	}
	if metadata.ArchivedAt == nil {
		archivedAt := s.now().UTC()
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET archived_at = ? WHERE id = ?", encodeTime(archivedAt), string(id)); err != nil {
			return session.Metadata{}, fmt.Errorf("archive session %q: %w", id, err)
		}
		metadata.ArchivedAt = &archivedAt
	}
	if err := tx.Commit(); err != nil {
		return session.Metadata{}, fmt.Errorf("commit archive of session %q: %w", id, err)
	}
	return metadata, nil
}

func (s *store) Restore(ctx context.Context, id session.ID) (session.Metadata, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return session.Metadata{}, err
	}
	defer tx.Rollback()
	metadata, err := metadataByID(ctx, tx, id)
	if err != nil {
		return session.Metadata{}, err
	}
	if metadata.ArchivedAt != nil {
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET archived_at = NULL WHERE id = ?", string(id)); err != nil {
			return session.Metadata{}, fmt.Errorf("restore session %q: %w", id, err)
		}
		metadata.ArchivedAt = nil
	}
	if err := tx.Commit(); err != nil {
		return session.Metadata{}, fmt.Errorf("commit restore of session %q: %w", id, err)
	}
	return metadata, nil
}

func (s *store) Delete(ctx context.Context, id session.ID) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE id = ?", string(id))
	if err != nil {
		return fmt.Errorf("delete session %q: %w", id, err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect deletion of session %q: %w", id, err)
	}
	if deleted == 0 {
		return notFound(id)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit deletion of session %q: %w", id, err)
	}
	return nil
}

func (s *store) Fork(ctx context.Context, source session.ID, request session.ForkRequest) (session.Metadata, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return session.Metadata{}, err
	}
	defer tx.Rollback()
	sourceMetadata, err := metadataByID(ctx, tx, source)
	if err != nil {
		return session.Metadata{}, err
	}
	targetID, err := s.generateID()
	if err != nil {
		return session.Metadata{}, fmt.Errorf("generate fork target ID: %w", err)
	}
	title := request.Title
	if title == "" {
		title = sourceMetadata.Title
	}
	now := s.now().UTC()
	target := session.Metadata{ID: targetID, Title: title, CreatedAt: now, UpdatedAt: now}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO sessions (id, title, created_at, updated_at, archived_at) VALUES (?, ?, ?, ?, NULL)",
		string(targetID), title, encodeTime(now), encodeTime(now),
	); err != nil {
		return session.Metadata{}, fmt.Errorf("create fork target for session %q: %w", source, err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO entries (session_id, sequence, kind, version, payload)
SELECT ?, sequence, kind, version, payload
FROM entries
WHERE session_id = ?
ORDER BY sequence`, string(targetID), string(source)); err != nil {
		return session.Metadata{}, fmt.Errorf("copy entries from session %q: %w", source, err)
	}
	if err := tx.Commit(); err != nil {
		return session.Metadata{}, fmt.Errorf("commit fork of session %q: %w", source, err)
	}
	return target, nil
}

// List returns active and archived sessions ordered by conversation recency,
// then creation recency, then stable identity.
func (s *store) List(ctx context.Context) ([]session.Metadata, error) {
	if ctx == nil {
		return nil, context.Canceled
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, title, created_at, updated_at, archived_at
FROM sessions
ORDER BY updated_at DESC, created_at DESC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	result := []session.Metadata{}
	for rows.Next() {
		metadata, err := scanMetadata(rows)
		if err != nil {
			return nil, fmt.Errorf("scan listed session: %w", err)
		}
		result = append(result, metadata)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate listed sessions: %w", err)
	}
	return result, nil
}

func (s *store) begin(ctx context.Context) (*sql.Tx, error) {
	if ctx == nil {
		return nil, context.Canceled
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return tx, nil
}

func (s *store) nextCommitTime(previous time.Time) time.Time {
	now := s.now().UTC()
	if !now.After(previous) {
		return previous.Add(time.Nanosecond)
	}
	return now
}

type metadataScanner interface {
	Scan(...any) error
}

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func metadataByID(ctx context.Context, queryer rowQueryer, id session.ID) (session.Metadata, error) {
	metadata, err := scanMetadata(queryer.QueryRowContext(ctx, `
SELECT id, title, created_at, updated_at, archived_at
FROM sessions
WHERE id = ?`, string(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return session.Metadata{}, notFound(id)
	}
	if err != nil {
		return session.Metadata{}, fmt.Errorf("get session %q metadata: %w", id, err)
	}
	return metadata, nil
}

func scanMetadata(scanner metadataScanner) (session.Metadata, error) {
	var (
		metadata   session.Metadata
		createdAt  int64
		updatedAt  int64
		archivedAt sql.NullInt64
	)
	if err := scanner.Scan(&metadata.ID, &metadata.Title, &createdAt, &updatedAt, &archivedAt); err != nil {
		return session.Metadata{}, err
	}
	metadata.CreatedAt = decodeTime(createdAt)
	metadata.UpdatedAt = decodeTime(updatedAt)
	if archivedAt.Valid {
		value := decodeTime(archivedAt.Int64)
		metadata.ArchivedAt = &value
	}
	return metadata, nil
}

func encodeTime(value time.Time) int64 { return value.UTC().UnixNano() }

func decodeTime(value int64) time.Time { return time.Unix(0, value).UTC() }

func randomID() (session.ID, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return session.ID(hex.EncodeToString(raw)), nil
}

func notFound(id session.ID) error {
	return fmt.Errorf("session %q: %w", id, session.ErrNotFound)
}

var (
	_ session.Store   = (*store)(nil)
	_ session.Manager = (*store)(nil)
	_ session.Query   = (*store)(nil)
)

package platform

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

type AuditEvent struct {
	Actor     string
	Action    string
	Outcome   string
	CreatedAt time.Time
}

type AuditStore struct {
	db *sql.DB
}

const maxAuditEvents = 100_000

func OpenAuditStore(path string) (*AuditStore, error) {
	if path != ":memory:" {
		for _, candidate := range databaseFiles(path) {
			if info, err := os.Lstat(candidate); err == nil && info.Mode()&os.ModeSymlink != 0 {
				return nil, errors.New("audit database files must not be symbolic links")
			} else if err != nil && !os.IsNotExist(err) {
				return nil, fmt.Errorf("inspect audit database file: %w", err)
			}
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open audit database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if _, err := db.Exec(`
		PRAGMA journal_mode = WAL;
		PRAGMA busy_timeout = 5000;
		CREATE TABLE IF NOT EXISTS audit_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			actor TEXT NOT NULL,
			action TEXT NOT NULL,
			outcome TEXT NOT NULL,
			created_at TEXT NOT NULL
		);`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate audit database: %w", err)
	}
	if path != ":memory:" {
		for _, candidate := range databaseFiles(path) {
			if err := os.Chmod(candidate, 0o600); err != nil && !os.IsNotExist(err) {
				_ = db.Close()
				return nil, fmt.Errorf("secure audit database permissions: %w", err)
			}
		}
	}

	return &AuditStore{db: db}, nil
}

func databaseFiles(path string) []string {
	return []string{path, path + "-wal", path + "-shm"}
}

func (s *AuditStore) Record(ctx context.Context, event AuditEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("record audit event: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO audit_events (actor, action, outcome, created_at) VALUES (?, ?, ?, ?)",
		event.Actor, event.Action, event.Outcome, event.CreatedAt.UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("record audit event: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM audit_events WHERE id <= (SELECT COALESCE(MAX(id), 0) - ? FROM audit_events)",
		maxAuditEvents,
	); err != nil {
		return fmt.Errorf("record audit event: prune retained events: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("record audit event: commit: %w", err)
	}
	return nil
}

func (s *AuditStore) Close() error {
	return s.db.Close()
}

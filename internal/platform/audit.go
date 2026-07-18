package platform

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

type AuditEvent struct {
	ID        int64
	Actor     string
	Action    string
	Outcome   string
	CreatedAt time.Time
}

type AuditPage struct {
	Events       []AuditEvent
	NextBeforeID int64
	HasMore      bool
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
		for _, warning := range auditFilePermissionWarnings(databaseFiles(path), os.Geteuid()) {
			log.Print(warning)
		}
	}

	return &AuditStore{db: db}, nil
}

func auditFilePermissionWarnings(paths []string, effectiveUID int) []string {
	warnings := make([]string, 0)
	for _, candidate := range paths {
		info, err := os.Lstat(candidate)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			warnings = append(warnings, "WARNING: could not inspect audit database permissions; relying on the running user's operating-system permissions")
			continue
		}
		if info.Mode().Perm()&0o077 != 0 {
			warnings = append(warnings, "WARNING: audit database permissions are broader than 0600; relying on the running user's operating-system permissions")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Uid) != effectiveUID {
			warnings = append(warnings, "WARNING: audit database owner differs from the running user; relying on the running user's operating-system permissions")
		}
	}
	return warnings
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

func (s *AuditStore) List(ctx context.Context, beforeID int64, limit int) (AuditPage, error) {
	if beforeID < 0 || limit < 1 || limit > 100 {
		return AuditPage{}, errors.New("list audit events: invalid query bounds")
	}
	query := "SELECT id, actor, action, outcome, created_at FROM audit_events ORDER BY id DESC LIMIT ?"
	arguments := []any{limit + 1}
	if beforeID > 0 {
		query = "SELECT id, actor, action, outcome, created_at FROM audit_events WHERE id < ? ORDER BY id DESC LIMIT ?"
		arguments = []any{beforeID, limit + 1}
	}
	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return AuditPage{}, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()

	events := make([]AuditEvent, 0, limit+1)
	for rows.Next() {
		event, err := scanAuditEvent(rows)
		if err != nil {
			return AuditPage{}, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return AuditPage{}, fmt.Errorf("list audit events: iterate rows: %w", err)
	}

	page := AuditPage{Events: events}
	if len(events) > limit {
		page.Events = events[:limit]
		page.HasMore = true
		page.NextBeforeID = page.Events[len(page.Events)-1].ID
	}
	return page, nil
}

type auditEventScanner interface {
	Scan(...any) error
}

func scanAuditEvent(scanner auditEventScanner) (AuditEvent, error) {
	var event AuditEvent
	var createdAt string
	if err := scanner.Scan(&event.ID, &event.Actor, &event.Action, &event.Outcome, &createdAt); err != nil {
		return AuditEvent{}, fmt.Errorf("list audit events: scan row: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return AuditEvent{}, fmt.Errorf("parse audit event timestamp: %w", err)
	}
	event.CreatedAt = parsed.UTC()
	return event, nil
}

func (s *AuditStore) Close() error {
	return s.db.Close()
}

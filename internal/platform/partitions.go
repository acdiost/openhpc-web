package platform

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	partitionSourceManaged = "managed"
	partitionSourceSystem  = "system"
)

type PartitionSpec struct {
	Name      string
	Nodes     []string
	Managed   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

type PartitionPatch struct {
	AddedNodes   []string
	RemovedNodes []string
}

type PartitionChange struct {
	Spec    PartitionSpec
	Created bool
	Updated bool
	Patch   PartitionPatch
}

type PartitionStore struct {
	db *sql.DB
}

type partitionQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func OpenPartitionStore(path string) (*PartitionStore, error) {
	if path != ":memory:" {
		for _, candidate := range databaseFiles(path) {
			if info, err := os.Lstat(candidate); err == nil && info.Mode()&os.ModeSymlink != 0 {
				return nil, errors.New("partition database files must not be symbolic links")
			} else if err != nil && !os.IsNotExist(err) {
				return nil, fmt.Errorf("inspect partition database file: %w", err)
			}
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open partition database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`
		PRAGMA journal_mode = WAL;
		PRAGMA busy_timeout = 5000;
		PRAGMA foreign_keys = ON;
		CREATE TABLE IF NOT EXISTS slurm_partitions (
			name TEXT PRIMARY KEY,
			source TEXT NOT NULL DEFAULT 'managed',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS slurm_partition_nodes (
			partition_name TEXT NOT NULL,
			node_name TEXT NOT NULL,
			node_order INTEGER NOT NULL,
			PRIMARY KEY (partition_name, node_name),
			FOREIGN KEY (partition_name) REFERENCES slurm_partitions(name) ON DELETE CASCADE
		);`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate partition database: %w", err)
	}
	if err := ensurePartitionSourceColumn(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if path != ":memory:" {
		for _, warning := range auditFilePermissionWarnings(databaseFiles(path), os.Geteuid()) {
			log.Print(warning)
		}
	}
	return &PartitionStore{db: db}, nil
}

func ensurePartitionSourceColumn(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(slurm_partitions)`)
	if err != nil {
		return fmt.Errorf("inspect partition schema: %w", err)
	}
	defer rows.Close()

	hasSource := false
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			return fmt.Errorf("inspect partition schema: scan row: %w", err)
		}
		if name == "source" {
			hasSource = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect partition schema: iterate rows: %w", err)
	}
	if hasSource {
		return nil
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE slurm_partitions ADD COLUMN source TEXT NOT NULL DEFAULT 'managed'`); err != nil {
		return fmt.Errorf("migrate partition database: add source column: %w", err)
	}
	return nil
}

func (s *PartitionStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *PartitionStore) Get(ctx context.Context, name string) (PartitionSpec, bool, error) {
	if err := validatePartitionName(name); err != nil {
		return PartitionSpec{}, false, err
	}
	return loadPartition(ctx, s.db, name)
}

func (s *PartitionStore) List(ctx context.Context) ([]PartitionSpec, error) {
	return listPartitions(ctx, s.db, "")
}

func (s *PartitionStore) ListSystem(ctx context.Context) ([]PartitionSpec, error) {
	return listPartitions(ctx, s.db, partitionSourceSystem)
}

func (s *PartitionStore) ListManaged(ctx context.Context) ([]PartitionSpec, error) {
	return listPartitions(ctx, s.db, partitionSourceManaged)
}

func (s *PartitionStore) Upsert(ctx context.Context, spec PartitionSpec) (PartitionChange, error) {
	return s.upsert(ctx, spec, partitionSourceManaged)
}

func (s *PartitionStore) ImportSystem(ctx context.Context, spec PartitionSpec) (PartitionChange, error) {
	return s.upsert(ctx, spec, partitionSourceSystem)
}

func (s *PartitionStore) DeleteManaged(ctx context.Context, name string) error {
	if err := validatePartitionName(name); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete partition: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	existing, found, err := loadPartitionTx(ctx, tx, name)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if !existing.Managed {
		return fmt.Errorf("partition %s is read-only", name)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM slurm_partitions WHERE name = ?`, name); err != nil {
		return fmt.Errorf("delete partition: delete row: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete partition: commit: %w", err)
	}
	return nil
}

func (s *PartitionStore) upsert(ctx context.Context, spec PartitionSpec, source string) (PartitionChange, error) {
	if err := validatePartitionName(spec.Name); err != nil {
		return PartitionChange{}, err
	}
	if len(spec.Nodes) == 0 {
		return PartitionChange{}, errors.New("partition must include at least one node")
	}
	nodes, err := normalizePartitionNodes(spec.Nodes)
	if err != nil {
		return PartitionChange{}, err
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PartitionChange{}, fmt.Errorf("save partition: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	existing, found, err := loadPartitionTx(ctx, tx, spec.Name)
	if err != nil {
		return PartitionChange{}, err
	}
	managed := source == partitionSourceManaged
	if found {
		if existing.Managed != managed {
			if managed {
				return PartitionChange{}, fmt.Errorf("partition %s is read-only", spec.Name)
			}
			return PartitionChange{}, fmt.Errorf("partition %s is already managed by the platform", spec.Name)
		}
		added, removed := diffPartitionNodes(existing.Nodes, nodes)
		if len(added) == 0 && len(removed) == 0 {
			if err := tx.Commit(); err != nil {
				return PartitionChange{}, fmt.Errorf("save partition: commit: %w", err)
			}
			return PartitionChange{Spec: existing}, nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE slurm_partitions SET source = ?, updated_at = ? WHERE name = ?`, source, now.Format(time.RFC3339Nano), spec.Name); err != nil {
			return PartitionChange{}, fmt.Errorf("save partition: update partition: %w", err)
		}
		if err := replacePartitionNodes(ctx, tx, spec.Name, nodes); err != nil {
			return PartitionChange{}, err
		}
		if err := tx.Commit(); err != nil {
			return PartitionChange{}, fmt.Errorf("save partition: commit: %w", err)
		}
		return PartitionChange{
			Spec:    PartitionSpec{Name: spec.Name, Nodes: nodes, Managed: managed, CreatedAt: existing.CreatedAt, UpdatedAt: now},
			Updated: true,
			Patch:   PartitionPatch{AddedNodes: added, RemovedNodes: removed},
		}, nil
	}

	timestamp := now.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO slurm_partitions(name, source, created_at, updated_at) VALUES (?, ?, ?, ?)`, spec.Name, source, timestamp, timestamp); err != nil {
		return PartitionChange{}, fmt.Errorf("save partition: insert partition: %w", err)
	}
	for index, node := range nodes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO slurm_partition_nodes(partition_name, node_name, node_order) VALUES (?, ?, ?)`, spec.Name, node, index); err != nil {
			return PartitionChange{}, fmt.Errorf("save partition: insert partition node: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return PartitionChange{}, fmt.Errorf("save partition: commit: %w", err)
	}
	return PartitionChange{
		Spec:    PartitionSpec{Name: spec.Name, Nodes: nodes, Managed: managed, CreatedAt: now, UpdatedAt: now},
		Created: true,
	}, nil
}

func listPartitions(ctx context.Context, queryer partitionQueryer, source string) ([]PartitionSpec, error) {
	args := make([]any, 0, 1)
	query := "SELECT name, source, created_at, updated_at FROM slurm_partitions"
	if source != "" {
		query += " WHERE source = ?"
		args = append(args, source)
	}
	query += " ORDER BY name"

	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list partitions: %w", err)
	}
	defer rows.Close()

	type record struct {
		name, source, createdAt, updatedAt string
	}
	records := make([]record, 0)
	for rows.Next() {
		var rec record
		if err := rows.Scan(&rec.name, &rec.source, &rec.createdAt, &rec.updatedAt); err != nil {
			return nil, fmt.Errorf("list partitions: scan row: %w", err)
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list partitions: iterate rows: %w", err)
	}

	partitions := make([]PartitionSpec, 0, len(records))
	for _, record := range records {
		spec, _, err := loadPartitionByMetadata(ctx, queryer, record.name, record.source, record.createdAt, record.updatedAt)
		if err != nil {
			return nil, err
		}
		partitions = append(partitions, spec)
	}
	return partitions, nil
}

func loadPartition(ctx context.Context, queryer partitionQueryer, name string) (PartitionSpec, bool, error) {
	var source, createdAt, updatedAt string
	if err := queryer.QueryRowContext(ctx, `SELECT source, created_at, updated_at FROM slurm_partitions WHERE name = ?`, name).Scan(&source, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PartitionSpec{}, false, nil
		}
		return PartitionSpec{}, false, fmt.Errorf("read partition: %w", err)
	}
	return loadPartitionByMetadata(ctx, queryer, name, source, createdAt, updatedAt)
}

func loadPartitionTx(ctx context.Context, tx *sql.Tx, name string) (PartitionSpec, bool, error) {
	return loadPartition(ctx, tx, name)
}

func loadPartitionByMetadata(ctx context.Context, queryer partitionQueryer, name, source, createdAt, updatedAt string) (PartitionSpec, bool, error) {
	nodes, err := partitionNodes(ctx, queryer, name)
	if err != nil {
		return PartitionSpec{}, false, err
	}

	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return PartitionSpec{}, false, fmt.Errorf("parse partition timestamp: %w", err)
	}
	updated, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return PartitionSpec{}, false, fmt.Errorf("parse partition timestamp: %w", err)
	}
	return PartitionSpec{Name: name, Nodes: nodes, Managed: source == partitionSourceManaged, CreatedAt: created.UTC(), UpdatedAt: updated.UTC()}, true, nil
}

func replacePartitionNodes(ctx context.Context, tx *sql.Tx, name string, nodes []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM slurm_partition_nodes WHERE partition_name = ?`, name); err != nil {
		return fmt.Errorf("save partition: clear partition nodes: %w", err)
	}
	for index, node := range nodes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO slurm_partition_nodes(partition_name, node_name, node_order) VALUES (?, ?, ?)`, name, node, index); err != nil {
			return fmt.Errorf("save partition: add partition node: %w", err)
		}
	}
	return nil
}

func partitionNodes(ctx context.Context, queryer partitionQueryer, name string) ([]string, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT node_name FROM slurm_partition_nodes WHERE partition_name = ? ORDER BY node_order, node_name`, name)
	if err != nil {
		return nil, fmt.Errorf("list partition nodes: %w", err)
	}
	defer rows.Close()

	nodes := make([]string, 0)
	for rows.Next() {
		var node string
		if err := rows.Scan(&node); err != nil {
			return nil, fmt.Errorf("list partition nodes: scan row: %w", err)
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list partition nodes: iterate rows: %w", err)
	}
	return normalizePartitionNodes(nodes)
}

func normalizePartitionNodes(nodes []string) ([]string, error) {
	normalized := make([]string, 0, len(nodes))
	seen := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		node = strings.TrimSpace(node)
		if err := validatePartitionNodeName(node); err != nil {
			return nil, err
		}
		if _, found := seen[node]; found {
			return nil, errors.New("partition nodes must be unique")
		}
		seen[node] = struct{}{}
		normalized = append(normalized, node)
	}
	sort.Strings(normalized)
	return normalized, nil
}

func diffPartitionNodes(existing, desired []string) ([]string, []string) {
	existingSet := make(map[string]struct{}, len(existing))
	for _, node := range existing {
		existingSet[node] = struct{}{}
	}
	desiredSet := make(map[string]struct{}, len(desired))
	for _, node := range desired {
		desiredSet[node] = struct{}{}
	}
	added := make([]string, 0)
	for _, node := range desired {
		if _, found := existingSet[node]; !found {
			added = append(added, node)
		}
	}
	removed := make([]string, 0)
	for _, node := range existing {
		if _, found := desiredSet[node]; !found {
			removed = append(removed, node)
		}
	}
	return added, removed
}

func validatePartitionName(name string) error {
	if !validPartitionIdentifier(name) {
		return errors.New("invalid partition name")
	}
	return nil
}

func validatePartitionNodeName(name string) error {
	if !validPartitionIdentifier(name) {
		return errors.New("invalid partition node name")
	}
	return nil
}

func validPartitionIdentifier(value string) bool {
	if value == "" || len(value) > 64 || value == "." || value == ".." || strings.TrimSpace(value) != value {
		return false
	}
	return !strings.ContainsAny(value, "/\\\x00\r\n\t ")
}

package platform

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

type UserRole string

const (
	RoleAdmin UserRole = "admin"
	RoleUser  UserRole = "user"
)

type PlatformUser struct {
	Username     string
	PasswordHash string
	Role         UserRole
	Enabled      bool
	CreatedAt    time.Time
}

type UserStore struct{ db *sql.DB }

var platformUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

var ErrUserExists = errors.New("platform user already exists")

func OpenUserStore(path string) (*UserStore, error) {
	if path != ":memory:" {
		for _, candidate := range databaseFiles(path) {
			if info, err := os.Lstat(candidate); err == nil && info.Mode()&os.ModeSymlink != 0 {
				return nil, errors.New("platform user database files must not be symbolic links")
			} else if err != nil && !os.IsNotExist(err) {
				return nil, fmt.Errorf("inspect platform user database file: %w", err)
			}
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open platform user database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`
		PRAGMA busy_timeout = 5000;
		CREATE TABLE IF NOT EXISTS platform_users (
			username TEXT PRIMARY KEY,
			password_hash TEXT NOT NULL,
			role TEXT NOT NULL CHECK (role IN ('admin', 'user')),
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL
		);`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate platform users: %w", err)
	}
	return &UserStore{db: db}, nil
}

func (s *UserStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func ValidateUsername(username string) error {
	if !platformUsernamePattern.MatchString(username) || username == "." || username == ".." {
		return errors.New("username must be 1-64 letters, digits, dot, underscore, or hyphen")
	}
	return nil
}

func ValidateRole(role UserRole) error {
	if role != RoleAdmin && role != RoleUser {
		return errors.New("unsupported platform user role")
	}
	return nil
}

func (s *UserStore) Upsert(ctx context.Context, user PlatformUser) error {
	if err := ValidateUsername(user.Username); err != nil {
		return err
	}
	if err := ValidateRole(user.Role); err != nil {
		return err
	}
	if strings.TrimSpace(user.PasswordHash) == "" {
		return errors.New("password hash is required")
	}
	if user.CreatedAt.IsZero() {
		user.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO platform_users(username,password_hash,role,enabled,created_at) VALUES(?,?,?,?,?)
		ON CONFLICT(username) DO UPDATE SET password_hash=excluded.password_hash, role=excluded.role, enabled=excluded.enabled`,
		user.Username, user.PasswordHash, user.Role, user.Enabled, user.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("save platform user: %w", err)
	}
	return nil
}

func (s *UserStore) Create(ctx context.Context, user PlatformUser) error {
	if err := ValidateUsername(user.Username); err != nil {
		return err
	}
	if err := ValidateRole(user.Role); err != nil {
		return err
	}
	if strings.TrimSpace(user.PasswordHash) == "" {
		return errors.New("password hash is required")
	}
	if user.CreatedAt.IsZero() {
		user.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO platform_users(username,password_hash,role,enabled,created_at) VALUES(?,?,?,?,?)`,
		user.Username, user.PasswordHash, user.Role, user.Enabled, user.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return ErrUserExists
	}
	return fmt.Errorf("create platform user: %w", err)
}

func (s *UserStore) Get(ctx context.Context, username string) (PlatformUser, bool, error) {
	if err := ValidateUsername(username); err != nil {
		return PlatformUser{}, false, err
	}
	var user PlatformUser
	var role, created string
	err := s.db.QueryRowContext(ctx, "SELECT username,password_hash,role,enabled,created_at FROM platform_users WHERE username = ?", username).
		Scan(&user.Username, &user.PasswordHash, &role, &user.Enabled, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return PlatformUser{}, false, nil
	}
	if err != nil {
		return PlatformUser{}, false, fmt.Errorf("read platform user: %w", err)
	}
	user.Role = UserRole(role)
	user.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return PlatformUser{}, false, fmt.Errorf("parse platform user timestamp: %w", err)
	}
	return user, true, nil
}

func (s *UserStore) List(ctx context.Context) ([]PlatformUser, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT username,password_hash,role,enabled,created_at FROM platform_users ORDER BY username")
	if err != nil {
		return nil, fmt.Errorf("list platform users: %w", err)
	}
	defer rows.Close()
	users := make([]PlatformUser, 0)
	for rows.Next() {
		var user PlatformUser
		var role, created string
		if err := rows.Scan(&user.Username, &user.PasswordHash, &role, &user.Enabled, &created); err != nil {
			return nil, err
		}
		user.Role = UserRole(role)
		user.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Username < users[j].Username })
	return users, nil
}

func (s *UserStore) SetEnabled(ctx context.Context, username string, enabled bool) error {
	if err := ValidateUsername(username); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, "UPDATE platform_users SET enabled = ? WHERE username = ?", enabled, username)
	if err != nil {
		return fmt.Errorf("update platform user: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

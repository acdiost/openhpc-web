package platform

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"time"
)

const maxSettingValueBytes = 8192

var ErrSettingsKeyRequired = errors.New("settings encryption key is required")

var settingSchema = map[string]bool{
	"OPENHPC_SLURM_ENABLED": false, "OPENHPC_SLURM_BIN_DIR": false, "OPENHPC_SLURM_TIMEOUT": false,
	"OPENHPC_SLURM_MAX_OUTPUT": false, "OPENHPC_SLURM_CACHE_TTL": false, "OPENHPC_JOB_OUTPUT_ROOTS": false,
	"OPENHPC_LDAP_ENABLED": false, "OPENHPC_LDAP_URL": false, "OPENHPC_LDAP_BASE_DN": false,
	"OPENHPC_LDAP_USER_BASE_DN": false, "OPENHPC_LDAP_GROUP_BASE_DN": false, "OPENHPC_LDAP_BIND_DN": false,
	"OPENHPC_LDAP_BIND_PASSWORD": true, "OPENHPC_LDAP_CA_FILE": false, "OPENHPC_LDAP_TIMEOUT": false,
	"OPENHPC_LDAP_MAX_RESULTS": false,
}

func KnownSettingKeys() []string {
	keys := make([]string, 0, len(settingSchema))
	for key := range settingSchema {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func IsSecretSetting(key string) bool { return settingSchema[key] }

type Setting struct {
	Key        string
	Value      string
	Secret     bool
	Configured bool
}

type SettingsStore struct {
	db  *sql.DB
	key []byte
}

func OpenSettingsStore(path string, key []byte) (*SettingsStore, error) {
	if len(key) != 0 && len(key) != 32 {
		return nil, errors.New("settings encryption key must be 32 bytes")
	}
	if path != ":memory:" {
		for _, candidate := range databaseFiles(path) {
			if info, err := os.Lstat(candidate); err == nil && info.Mode()&os.ModeSymlink != 0 {
				return nil, errors.New("settings database files must not be symbolic links")
			} else if err != nil && !os.IsNotExist(err) {
				return nil, fmt.Errorf("inspect settings database file: %w", err)
			}
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open settings database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`
		PRAGMA journal_mode = WAL;
		PRAGMA busy_timeout = 5000;
		CREATE TABLE IF NOT EXISTS app_settings (
			key TEXT PRIMARY KEY,
			value BLOB NOT NULL,
			secret INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL
		);`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate settings database: %w", err)
	}
	if path != ":memory:" {
		for _, warning := range auditFilePermissionWarnings(databaseFiles(path), os.Geteuid()) {
			log.Print(warning)
		}
	}
	return &SettingsStore{db: db, key: append([]byte(nil), key...)}, nil
}

func (s *SettingsStore) Get(ctx context.Context, key string) (string, bool, error) {
	if err := validateSettingKey(key); err != nil {
		return "", false, err
	}
	var raw []byte
	var secret bool
	err := s.db.QueryRowContext(ctx, "SELECT value, secret FROM app_settings WHERE key = ?", key).Scan(&raw, &secret)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read setting: %w", err)
	}
	if settingSchema[key] {
		value, err := s.decrypt(key, raw)
		if err != nil {
			return "", false, err
		}
		return value, true, nil
	}
	return string(raw), true, nil
}

func (s *SettingsStore) List(ctx context.Context, keys map[string]bool) ([]Setting, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT key, value, secret FROM app_settings ORDER BY key")
	if err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	defer rows.Close()
	result := make([]Setting, 0)
	for rows.Next() {
		var key string
		var raw []byte
		var secret bool
		if err := rows.Scan(&key, &raw, &secret); err != nil {
			return nil, fmt.Errorf("scan setting: %w", err)
		}
		if len(keys) > 0 && !keys[key] {
			continue
		}
		secret = settingSchema[key]
		entry := Setting{Key: key, Secret: secret, Configured: true}
		if !secret {
			entry.Value = string(raw)
		}
		result = append(result, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	return result, nil
}

func (s *SettingsStore) Set(ctx context.Context, key, value string) error {
	return s.SetMany(ctx, map[string]string{key: value})
}

func (s *SettingsStore) SetMany(ctx context.Context, values map[string]string) error {
	prepared := make(map[string]struct {
		raw    []byte
		secret bool
	}, len(values))
	for key, value := range values {
		if err := validateSettingKey(key); err != nil {
			return err
		}
		if len(value) > maxSettingValueBytes {
			return errors.New("setting value is too long")
		}
		secret := settingSchema[key]
		var raw []byte
		var err error
		if secret {
			if value != "" {
				if len(s.key) == 0 {
					return ErrSettingsKeyRequired
				}
				raw, err = s.encrypt(key, value)
			}
		} else {
			raw = []byte(value)
		}
		if err != nil {
			return err
		}
		prepared[key] = struct {
			raw    []byte
			secret bool
		}{raw: raw, secret: secret}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("save settings: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for key, value := range prepared {
		var execErr error
		if len(value.raw) == 0 {
			_, execErr = tx.ExecContext(ctx, "DELETE FROM app_settings WHERE key = ?", key)
		} else {
			_, execErr = tx.ExecContext(ctx, `INSERT INTO app_settings (key, value, secret, updated_at) VALUES (?, ?, ?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value, secret=excluded.secret, updated_at=excluded.updated_at`, key, value.raw, value.secret, time.Now().UTC().Format(time.RFC3339Nano))
		}
		if execErr != nil {
			return fmt.Errorf("save setting: %w", execErr)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("save settings: commit: %w", err)
	}
	return nil
}

func (s *SettingsStore) Close() error { return s.db.Close() }

func (s *SettingsStore) encrypt(key, value string) ([]byte, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, errors.New("invalid settings encryption key")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("initialize settings encryption")
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, errors.New("generate settings encryption nonce")
	}
	return gcm.Seal(nonce, nonce, []byte(value), []byte(key)), nil
}

func (s *SettingsStore) decrypt(key string, raw []byte) (string, error) {
	if len(s.key) == 0 {
		return "", ErrSettingsKeyRequired
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return "", errors.New("invalid settings encryption key")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(raw) < gcm.NonceSize() {
		return "", errors.New("invalid encrypted setting")
	}
	value, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], []byte(key))
	if err != nil {
		return "", errors.New("cannot decrypt setting")
	}
	return string(value), nil
}

func validateSettingKey(key string) error {
	if _, ok := settingSchema[key]; !ok {
		return errors.New("unknown setting key")
	}
	if strings.TrimSpace(key) == "" || len(key) > 128 || strings.TrimSpace(key) != key {
		return errors.New("invalid setting key")
	}
	return nil
}

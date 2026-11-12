package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS settings (
	scope TEXT NOT NULL,
	key   TEXT NOT NULL,
	value TEXT NOT NULL,
	PRIMARY KEY (scope, key)
);

CREATE TABLE IF NOT EXISTS providers (
	name    TEXT PRIMARY KEY,
	command TEXT NOT NULL,
	config  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS frontends (
	name    TEXT PRIMARY KEY,
	command TEXT NOT NULL,
	config  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
	id         TEXT PRIMARY KEY,
	dir        TEXT NOT NULL,
	title      TEXT NOT NULL,
	created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS sessions_by_dir ON sessions (dir, created_at);

CREATE TABLE IF NOT EXISTS entries (
	session_id TEXT NOT NULL REFERENCES sessions (id),
	seq        INTEGER NOT NULL,
	kind       TEXT NOT NULL,
	payload    TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (session_id, seq)
);
`

// SQLite is the Store backed by a single SQLite file.
type SQLite struct {
	db *sql.DB
}

// Open opens the database at path, creating the file and schema if needed.
func Open(path string) (*SQLite, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}

	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)

	if err != nil {
		return nil, err
	}

	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}

	return &SQLite{db: db}, nil
}

// Close implements Store.
func (s *SQLite) Close() error {
	return s.db.Close()
}

// Setting implements Store.
func (s *SQLite) Setting(scope, key string) (string, bool, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE scope = ? AND key = ?`, scope, key).Scan(&value)

	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}

	return value, err == nil, err
}

// SetSetting implements Store.
func (s *SQLite) SetSetting(scope, key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO settings (scope, key, value) VALUES (?, ?, ?)
		 ON CONFLICT (scope, key) DO UPDATE SET value = excluded.value`,
		scope, key, value,
	)

	return err
}

// Settings implements Store.
func (s *SQLite) Settings() ([]Setting, error) {
	rows, err := s.db.Query(`SELECT scope, key, value FROM settings ORDER BY scope, key`)

	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var settings []Setting

	for rows.Next() {
		var setting Setting

		if err := rows.Scan(&setting.Scope, &setting.Key, &setting.Value); err != nil {
			return nil, err
		}

		settings = append(settings, setting)
	}

	return settings, rows.Err()
}

// programTable returns the table that holds a kind of program. Each kind has
// its own table with the same columns.
func programTable(kind string) (string, error) {
	switch kind {
	case KindProvider:
		return "providers", nil
	case KindFrontend:
		return "frontends", nil
	}

	return "", fmt.Errorf("unknown program kind %q", kind)
}

// Program implements Store.
func (s *SQLite) Program(kind, name string) (Program, error) {
	table, err := programTable(kind)

	if err != nil {
		return Program{}, err
	}

	row := s.db.QueryRow(`SELECT name, command, config FROM `+table+` WHERE name = ?`, name)

	return scanProgram(row)
}

// PutProgram implements Store.
func (s *SQLite) PutProgram(kind string, program Program) error {
	table, err := programTable(kind)

	if err != nil {
		return err
	}

	command, err := json.Marshal(program.Command)

	if err != nil {
		return err
	}

	config := program.Config

	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}

	_, err = s.db.Exec(
		`INSERT INTO `+table+` (name, command, config) VALUES (?, ?, ?)
		 ON CONFLICT (name) DO UPDATE SET command = excluded.command, config = excluded.config`,
		program.Name, string(command), string(config),
	)

	return err
}

// DeleteProgram implements Store.
func (s *SQLite) DeleteProgram(kind, name string) error {
	table, err := programTable(kind)

	if err != nil {
		return err
	}

	result, err := s.db.Exec(`DELETE FROM `+table+` WHERE name = ?`, name)

	if err != nil {
		return err
	}

	if count, _ := result.RowsAffected(); count == 0 {
		return ErrNotFound
	}

	return nil
}

// Programs implements Store.
func (s *SQLite) Programs(kind string) ([]Program, error) {
	table, err := programTable(kind)

	if err != nil {
		return nil, err
	}

	rows, err := s.db.Query(`SELECT name, command, config FROM ` + table + ` ORDER BY name`)

	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var programs []Program

	for rows.Next() {
		program, err := scanProgram(rows)

		if err != nil {
			return nil, err
		}

		programs = append(programs, program)
	}

	return programs, rows.Err()
}

// CreateSession implements Store.
func (s *SQLite) CreateSession(dir, title string) (Session, error) {
	raw := make([]byte, 4)

	if _, err := rand.Read(raw); err != nil {
		return Session{}, err
	}

	session := Session{ID: hex.EncodeToString(raw), Dir: dir, Title: title, CreatedAt: time.Now()}

	_, err := s.db.Exec(
		`INSERT INTO sessions (id, dir, title, created_at) VALUES (?, ?, ?, ?)`,
		session.ID, session.Dir, session.Title, session.CreatedAt.UnixMilli(),
	)

	return session, err
}

// Session implements Store.
func (s *SQLite) Session(id string) (Session, error) {
	row := s.db.QueryRow(`SELECT id, dir, title, created_at FROM sessions WHERE id = ?`, id)

	return scanSession(row)
}

// LatestSession implements Store.
func (s *SQLite) LatestSession(dir string) (Session, error) {
	row := s.db.QueryRow(
		`SELECT id, dir, title, created_at FROM sessions WHERE dir = ? ORDER BY created_at DESC LIMIT 1`,
		dir,
	)

	return scanSession(row)
}

// Sessions implements Store. An empty dir lists the sessions of every project.
func (s *SQLite) Sessions(dir string) ([]Session, error) {
	rows, err := s.db.Query(
		`SELECT id, dir, title, created_at FROM sessions WHERE ? = '' OR dir = ? ORDER BY created_at DESC`,
		dir, dir,
	)

	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var sessions []Session

	for rows.Next() {
		session, err := scanSession(rows)

		if err != nil {
			return nil, err
		}

		sessions = append(sessions, session)
	}

	return sessions, rows.Err()
}

// Append implements Store.
func (s *SQLite) Append(sessionID, kind string, payload any) (Entry, error) {
	raw, err := json.Marshal(payload)

	if err != nil {
		return Entry{}, err
	}

	entry := Entry{Kind: kind, Payload: raw, CreatedAt: time.Now()}

	err = s.db.QueryRow(
		`INSERT INTO entries (session_id, seq, kind, payload, created_at)
		 VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM entries WHERE session_id = ?), ?, ?, ?)
		 RETURNING seq`,
		sessionID, sessionID, kind, string(raw), entry.CreatedAt.UnixMilli(),
	).Scan(&entry.Seq)

	return entry, err
}

// Entries implements Store.
func (s *SQLite) Entries(sessionID string) ([]Entry, error) {
	rows, err := s.db.Query(
		`SELECT seq, kind, payload, created_at FROM entries WHERE session_id = ? ORDER BY seq`,
		sessionID,
	)

	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var entries []Entry

	for rows.Next() {
		var entry Entry
		var payload string
		var createdAt int64

		if err := rows.Scan(&entry.Seq, &entry.Kind, &payload, &createdAt); err != nil {
			return nil, err
		}

		entry.Payload = json.RawMessage(payload)
		entry.CreatedAt = time.UnixMilli(createdAt)
		entries = append(entries, entry)
	}

	return entries, rows.Err()
}

// scanner is the part of *sql.Row and *sql.Rows the scan helpers need.
type scanner interface {
	Scan(dest ...any) error
}

func scanProgram(row scanner) (Program, error) {
	var program Program
	var command, config string

	err := row.Scan(&program.Name, &command, &config)

	if errors.Is(err, sql.ErrNoRows) {
		return program, ErrNotFound
	}

	if err != nil {
		return program, err
	}

	program.Config = json.RawMessage(config)

	return program, json.Unmarshal([]byte(command), &program.Command)
}

func scanSession(row scanner) (Session, error) {
	var session Session
	var createdAt int64

	err := row.Scan(&session.ID, &session.Dir, &session.Title, &createdAt)

	if errors.Is(err, sql.ErrNoRows) {
		return session, ErrNotFound
	}

	session.CreatedAt = time.UnixMilli(createdAt)

	return session, err
}

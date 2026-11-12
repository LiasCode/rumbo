package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
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

// migrations take a database from one schema version to the next. The
// version is the number of migrations applied, kept in user_version. A
// migration only adds: tables, columns, indexes, and values for what it adds.
var migrations = []string{
	// 1: sessions belong to a project root, so they are found from any of
	// its subdirectories. Existing sessions take their own directory.
	`ALTER TABLE sessions ADD COLUMN project TEXT NOT NULL DEFAULT '';
	 UPDATE sessions SET project = dir WHERE project = '';
	 CREATE INDEX sessions_by_project ON sessions (project, created_at);`,

	// 2: results of project checks. The checks were removed, and nothing
	// reads this table, but databases at version 2 exist, so the step stays
	// and later migrations keep their numbers.
	`CREATE TABLE check_results (
		project    TEXT NOT NULL,
		dir        TEXT NOT NULL,
		command    TEXT NOT NULL,
		snapshot   TEXT NOT NULL,
		passed     INTEGER NOT NULL,
		output     TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		PRIMARY KEY (project, dir, command)
	 );`,
}

// sessionColumns are selected in the order scanSession reads them. Activity
// is when the session was last used: its newest entry, or its creation.
const sessionColumns = `id, dir, project, title, created_at,
	COALESCE((SELECT MAX(created_at) FROM entries WHERE session_id = sessions.id), created_at) AS activity`

// SQLite is the Store backed by a single SQLite file.
type SQLite struct {
	db   *sql.DB
	path string
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

	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("upgrade database %s: %w", path, err)
	}

	return &SQLite{db: db, path: path}, nil
}

// migrate applies the migrations the database has not seen. It holds the
// write lock while it looks and while it works, so two processes starting
// together do not both apply the same one.
func migrate(db *sql.DB) error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)

	if err != nil {
		return err
	}

	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}

	var version int

	if err := conn.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		return err
	}

	if version > len(migrations) {
		_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		return fmt.Errorf("it was written by a newer rumbo (schema %d, this one knows %d)", version, len(migrations))
	}

	for ; version < len(migrations); version++ {
		if _, err := conn.ExecContext(ctx, migrations[version]); err != nil {
			_, _ = conn.ExecContext(ctx, `ROLLBACK`)
			return fmt.Errorf("migration %d: %w", version+1, err)
		}
	}

	// PRAGMA takes no parameters; version is a loop counter, not input.
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, version)); err != nil {
		_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		return err
	}

	_, err = conn.ExecContext(ctx, `COMMIT`)

	return err
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
func (s *SQLite) CreateSession(dir, project, title string) (Session, error) {
	raw := make([]byte, 4)

	if _, err := rand.Read(raw); err != nil {
		return Session{}, err
	}

	session := Session{ID: hex.EncodeToString(raw), Dir: dir, Project: project, Title: title, CreatedAt: time.Now()}

	_, err := s.db.Exec(
		`INSERT INTO sessions (id, dir, project, title, created_at) VALUES (?, ?, ?, ?, ?)`,
		session.ID, session.Dir, session.Project, session.Title, session.CreatedAt.UnixMilli(),
	)

	return session, err
}

// Session implements Store.
func (s *SQLite) Session(id string) (Session, error) {
	row := s.db.QueryRow(`SELECT `+sessionColumns+` FROM sessions WHERE id = ?`, id)

	return scanSession(row)
}

// LatestSession implements Store.
func (s *SQLite) LatestSession(project string) (Session, error) {
	row := s.db.QueryRow(
		`SELECT `+sessionColumns+` FROM sessions WHERE project = ? ORDER BY activity DESC LIMIT 1`,
		project,
	)

	return scanSession(row)
}

// Sessions implements Store.
func (s *SQLite) Sessions(project string) ([]Session, error) {
	rows, err := s.db.Query(
		`SELECT `+sessionColumns+` FROM sessions WHERE ? = '' OR project = ? ORDER BY activity DESC`,
		project, project,
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

// LockSession implements Store. The lock is a file lock next to the
// database, because the system drops it when the process dies, however it
// dies; a row in a table would outlive a crash.
func (s *SQLite) LockSession(id string) (func(), error) {
	dir := filepath.Join(filepath.Dir(s.path), "locks")

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	file, err := os.OpenFile(filepath.Join(dir, id+".lock"), os.O_CREATE|os.O_RDWR, 0o600)

	if err != nil {
		return nil, err
	}

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()

		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}

		return nil, err
	}

	return func() { _ = file.Close() }, nil
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
	var createdAt, activity int64

	err := row.Scan(&session.ID, &session.Dir, &session.Project, &session.Title, &createdAt, &activity)

	if errors.Is(err, sql.ErrNoRows) {
		return session, ErrNotFound
	}

	session.CreatedAt = time.UnixMilli(createdAt)
	session.ActiveAt = time.UnixMilli(activity)

	return session, err
}

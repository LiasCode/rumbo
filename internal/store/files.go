package store

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Files is the Store kept in files. What the user chose is one YAML file
// they can read and edit. Each session is a directory under data:
//
//	sessions/<id>/session.json    what the session is, and what it chose
//	sessions/<id>/entries.jsonl   its entries, one to a line, only appended
type Files struct {
	// config is the path of the configuration file.
	config string
	// data is the directory the sessions are kept in.
	data string

	// mu guards ends, and keeps this process's appends one after another.
	mu sync.Mutex
	// ends is where each session's entries ended when this process last
	// looked, so an append does not read them all to number the next one.
	ends map[string]end
}

// end is the last entry of a session and the size of its file with it.
type end struct {
	seq  int64
	size int64
}

const (
	sessionFile = "session.json"
	entriesFile = "entries.jsonl"

	// timeLayout writes a time in UTC to the millisecond.
	timeLayout = "2006-01-02T15:04:05.000Z"
)

// sessionRecord is session.json. The session's id is its directory's name.
type sessionRecord struct {
	Dir       string          `json:"dir"`
	Title     string          `json:"title"`
	Provider  string          `json:"provider,omitempty"`
	Model     string          `json:"model,omitempty"`
	Options   json.RawMessage `json:"options,omitempty"`
	Parent    string          `json:"parent,omitempty"`
	ParentSeq int64           `json:"parent_seq,omitempty"`
	CreatedAt string          `json:"created_at"`
}

// entryRecord is one line of entries.jsonl.
type entryRecord struct {
	Seq     int64           `json:"seq"`
	Time    string          `json:"time"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// Open returns the store whose configuration is the file at config and whose
// sessions are under data. Neither has to exist: each is made when something
// is first written to it.
func Open(config, data string) *Files {
	return &Files{config: config, data: data, ends: map[string]end{}}
}

// Close implements Store.
func (f *Files) Close() error {
	return nil
}

func (f *Files) sessionsDir() string {
	return filepath.Join(f.data, "sessions")
}

// sessionDir is the directory of a session. A name that could be anything
// but a directory of sessions is no session's.
func (f *Files) sessionDir(id string) (string, error) {
	if id == "" || strings.HasPrefix(id, ".") || strings.ContainsAny(id, `/\`) {
		return "", ErrNotFound
	}

	return filepath.Join(f.sessionsDir(), id), nil
}

// newID returns the id of a new session.
func newID() (string, error) {
	raw := make([]byte, 4)

	if _, err := rand.Read(raw); err != nil {
		return "", err
	}

	return hex.EncodeToString(raw), nil
}

// writeFile replaces the file at path with data in one step, so it is never
// read half written.
func writeFile(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")

	if err != nil {
		return err
	}

	defer os.Remove(temp.Name())

	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}

	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}

	if err := temp.Close(); err != nil {
		return err
	}

	return os.Rename(temp.Name(), path)
}

// locked runs change while it holds the directory, so two processes that
// read a file there to write it back do not lose each other's change.
func locked(dir string, change func() error) error {
	held, err := os.Open(dir)

	if err != nil {
		return err
	}

	defer held.Close()

	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}

	return change()
}

// create makes a session with the entries given. It is made under another
// name and then renamed, so it is only ever seen whole.
func (f *Files) create(session Session, entries []byte) (Session, error) {
	id, err := newID()

	if err != nil {
		return Session{}, err
	}

	session.ID = id
	root := f.sessionsDir()

	if err := os.MkdirAll(root, 0o700); err != nil {
		return Session{}, err
	}

	staging, err := os.MkdirTemp(root, ".new-")

	if err != nil {
		return Session{}, err
	}

	defer os.RemoveAll(staging)

	log := filepath.Join(staging, entriesFile)

	if err := os.WriteFile(log, entries, 0o600); err != nil {
		return Session{}, err
	}

	if err := os.Chtimes(log, session.ActiveAt, session.ActiveAt); err != nil {
		return Session{}, err
	}

	if err := writeSession(staging, session); err != nil {
		return Session{}, err
	}

	return session, os.Rename(staging, filepath.Join(root, id))
}

// writeSession writes what a session is to its directory.
func writeSession(dir string, session Session) error {
	raw, err := json.MarshalIndent(sessionRecord{
		Dir:       session.Dir,
		Title:     session.Title,
		Provider:  session.Provider,
		Model:     session.Model,
		Options:   session.Options,
		Parent:    session.Parent,
		ParentSeq: session.ParentSeq,
		CreatedAt: session.CreatedAt.UTC().Format(timeLayout),
	}, "", "  ")

	if err != nil {
		return err
	}

	return writeFile(filepath.Join(dir, sessionFile), append(raw, '\n'))
}

// CreateSession implements Store.
func (f *Files) CreateSession(dir, title string) (Session, error) {
	now := time.Now()

	return f.create(Session{Dir: dir, Title: title, CreatedAt: now, ActiveAt: now}, nil)
}

// BranchSession implements Store.
func (f *Files) BranchSession(from string, upTo int64) (Session, error) {
	source, err := f.Session(from)

	if err != nil {
		return Session{}, err
	}

	entries, err := f.Entries(from)

	if err != nil {
		return Session{}, err
	}

	// The entries keep their numbers and times, so the branch's history
	// reads as the other's did up to that point.
	var copied bytes.Buffer

	for _, entry := range entries {
		if entry.Seq > upTo {
			break
		}

		line, err := encodeEntry(entry)

		if err != nil {
			return Session{}, err
		}

		copied.Write(line)
	}

	branch := source
	branch.Parent, branch.ParentSeq = source.ID, upTo
	branch.CreatedAt = time.Now()
	branch.ActiveAt = branch.CreatedAt

	return f.create(branch, copied.Bytes())
}

// Session implements Store.
func (f *Files) Session(id string) (Session, error) {
	dir, err := f.sessionDir(id)

	if err != nil {
		return Session{}, err
	}

	path := filepath.Join(dir, sessionFile)
	raw, err := os.ReadFile(path)

	if errors.Is(err, os.ErrNotExist) {
		return Session{}, ErrNotFound
	}

	if err != nil {
		return Session{}, err
	}

	var record sessionRecord

	if err := json.Unmarshal(raw, &record); err != nil {
		return Session{}, fmt.Errorf("%s: %w", path, err)
	}

	created, err := time.Parse(time.RFC3339, record.CreatedAt)

	if err != nil {
		return Session{}, fmt.Errorf("%s: created_at: %w", path, err)
	}

	// The file is written to be read by a person; the options go back to
	// the bytes they were given as.
	var options json.RawMessage

	if len(record.Options) > 0 {
		var compact bytes.Buffer

		if err := json.Compact(&compact, record.Options); err != nil {
			return Session{}, fmt.Errorf("%s: options: %w", path, err)
		}

		options = compact.Bytes()
	}

	session := Session{
		ID:        id,
		Dir:       record.Dir,
		Title:     record.Title,
		Provider:  record.Provider,
		Model:     record.Model,
		Options:   options,
		Parent:    record.Parent,
		ParentSeq: record.ParentSeq,
		CreatedAt: created,
		ActiveAt:  created,
	}

	// A session was last used when its entries last changed: each append
	// sets that time itself, since a file system's own clock is too coarse
	// to order sessions used within a few milliseconds of each other.
	if info, err := os.Stat(filepath.Join(dir, entriesFile)); err == nil && info.ModTime().After(created) {
		session.ActiveAt = info.ModTime()
	}

	return session, nil
}

// change rewrites what a session is, holding its directory meanwhile.
func (f *Files) change(id string, apply func(session *Session)) error {
	dir, err := f.sessionDir(id)

	if err != nil {
		return err
	}

	err = locked(dir, func() error {
		session, err := f.Session(id)

		if err != nil {
			return err
		}

		apply(&session)

		return writeSession(dir, session)
	})

	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}

	return err
}

// SetSessionModel implements Store.
func (f *Files) SetSessionModel(id, provider, model string) error {
	return f.change(id, func(session *Session) {
		session.Provider, session.Model = provider, model
	})
}

// SetSessionOptions implements Store.
func (f *Files) SetSessionOptions(id string, options json.RawMessage) error {
	return f.change(id, func(session *Session) {
		session.Options = options
	})
}

// RenameSession implements Store.
func (f *Files) RenameSession(id, title string) error {
	return f.change(id, func(session *Session) {
		session.Title = title
	})
}

// Sessions implements Store.
func (f *Files) Sessions(dir string) ([]Session, error) {
	listed, err := os.ReadDir(f.sessionsDir())

	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	var sessions []Session

	for _, each := range listed {
		// What starts with a dot is a session being made or deleted.
		if !each.IsDir() || strings.HasPrefix(each.Name(), ".") {
			continue
		}

		session, err := f.Session(each.Name())

		// One another process deleted since the directory was read.
		if errors.Is(err, ErrNotFound) {
			continue
		}

		if err != nil {
			return nil, err
		}

		if dir == "" || session.Dir == dir {
			sessions = append(sessions, session)
		}
	}

	slices.SortFunc(sessions, func(a, b Session) int {
		if order := b.ActiveAt.Compare(a.ActiveAt); order != 0 {
			return order
		}

		if order := b.CreatedAt.Compare(a.CreatedAt); order != 0 {
			return order
		}

		return strings.Compare(b.ID, a.ID)
	})

	return sessions, nil
}

// LatestSession implements Store.
func (f *Files) LatestSession(dir string) (Session, error) {
	sessions, err := f.Sessions(dir)

	if err != nil {
		return Session{}, err
	}

	if len(sessions) == 0 {
		return Session{}, ErrNotFound
	}

	return sessions[0], nil
}

// DeleteSession implements Store. The directory takes a name no session has
// before it is removed, so a session is never seen half deleted.
func (f *Files) DeleteSession(id string) error {
	dir, err := f.sessionDir(id)

	if err != nil {
		return err
	}

	suffix, err := newID()

	if err != nil {
		return err
	}

	doomed := filepath.Join(f.sessionsDir(), ".deleted-"+id+"-"+suffix)

	if err := os.Rename(dir, doomed); errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	} else if err != nil {
		return err
	}

	f.mu.Lock()
	delete(f.ends, id)
	f.mu.Unlock()

	return os.RemoveAll(doomed)
}

// LockSession implements Store. The lock is a file lock on the session's
// entries, because the system drops it when the process dies, however it
// dies; a mark in a file would outlive a crash.
func (f *Files) LockSession(id string) (func(), error) {
	dir, err := f.sessionDir(id)

	if err != nil {
		return nil, err
	}

	file, err := os.OpenFile(filepath.Join(dir, entriesFile), os.O_RDWR, 0)

	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}

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

// SessionInUse implements Store. A lock is taken on an open file, so trying
// it through another one fails while any process holds it, this one too.
func (f *Files) SessionInUse(id string) (bool, error) {
	release, err := f.LockSession(id)

	switch {
	case errors.Is(err, ErrLocked):
		return true, nil

	// A session that is not there is held by nobody.
	case errors.Is(err, ErrNotFound):
		return false, nil

	case err != nil:
		return false, err
	}

	release()

	return false, nil
}

// encodeEntry writes an entry as its line of the file.
func encodeEntry(entry Entry) ([]byte, error) {
	line, err := json.Marshal(entryRecord{
		Seq:     entry.Seq,
		Time:    entry.CreatedAt.UTC().Format(timeLayout),
		Kind:    entry.Kind,
		Payload: entry.Payload,
	})

	if err != nil {
		return nil, err
	}

	return append(line, '\n'), nil
}

// readEntries reads the entries in the file at path, and says how many of
// its bytes they take. A last line without its end is one a process died
// writing: it is no entry, and is left out of both.
func readEntries(path string) ([]Entry, int64, error) {
	raw, err := os.ReadFile(path)

	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}

	if err != nil {
		return nil, 0, err
	}

	var entries []Entry
	var whole int64

	for number := 1; ; number++ {
		line, rest, ended := bytes.Cut(raw, []byte("\n"))

		if !ended {
			break
		}

		var record entryRecord

		if err := json.Unmarshal(line, &record); err != nil {
			return nil, 0, fmt.Errorf("%s, line %d: %w", path, number, err)
		}

		created, err := time.Parse(time.RFC3339, record.Time)

		if err != nil {
			return nil, 0, fmt.Errorf("%s, line %d: %w", path, number, err)
		}

		entries = append(entries, Entry{Seq: record.Seq, Kind: record.Kind, Payload: record.Payload, CreatedAt: created})
		whole += int64(len(line)) + 1
		raw = rest
	}

	return entries, whole, nil
}

// Entries implements Store.
func (f *Files) Entries(sessionID string) ([]Entry, error) {
	dir, err := f.sessionDir(sessionID)

	// A session that is not there has no entries.
	if err != nil {
		return nil, nil
	}

	entries, _, err := readEntries(filepath.Join(dir, entriesFile))

	return entries, err
}

// Append implements Store. An entry is one write of one line, flushed to the
// disk before the next request depends on it.
func (f *Files) Append(sessionID, kind string, payload any) (Entry, error) {
	raw, err := json.Marshal(payload)

	if err != nil {
		return Entry{}, err
	}

	dir, err := f.sessionDir(sessionID)

	if err != nil {
		return Entry{}, fmt.Errorf("session %s: %w", sessionID, err)
	}

	path := filepath.Join(dir, entriesFile)

	f.mu.Lock()
	defer f.mu.Unlock()

	file, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0)

	if errors.Is(err, os.ErrNotExist) {
		return Entry{}, fmt.Errorf("session %s: %w", sessionID, ErrNotFound)
	}

	if err != nil {
		return Entry{}, err
	}

	defer file.Close()

	last, err := f.end(sessionID, file, path)

	if err != nil {
		return Entry{}, err
	}

	entry := Entry{Seq: last.seq + 1, Kind: kind, Payload: raw, CreatedAt: time.Now()}
	line, err := encodeEntry(entry)

	if err != nil {
		return Entry{}, err
	}

	if _, err := file.Write(line); err != nil {
		return Entry{}, err
	}

	if err := file.Sync(); err != nil {
		return Entry{}, err
	}

	// This is when the session was last used, which Session reads back.
	if err := os.Chtimes(path, entry.CreatedAt, entry.CreatedAt); err != nil {
		return Entry{}, err
	}

	f.ends[sessionID] = end{seq: entry.Seq, size: last.size + int64(len(line))}

	return entry, nil
}

// end finds where a session's entries end. What this process remembers
// holds while the file has the size it left it with; otherwise the file is
// read, and a line a process died writing is cut off, so the next entry
// starts a line of its own. The caller holds the lock.
func (f *Files) end(sessionID string, file *os.File, path string) (end, error) {
	info, err := file.Stat()

	if err != nil {
		return end{}, err
	}

	if known, found := f.ends[sessionID]; found && known.size == info.Size() {
		return known, nil
	}

	entries, whole, err := readEntries(path)

	if err != nil {
		return end{}, err
	}

	if whole < info.Size() {
		if err := file.Truncate(whole); err != nil {
			return end{}, err
		}
	}

	found := end{size: whole}

	if len(entries) > 0 {
		found.seq = entries[len(entries)-1].Seq
	}

	return found, nil
}

// Recent implements Store.
func (f *Files) Recent(dir string, kinds []string, limit int) ([]Entry, error) {
	if len(kinds) == 0 {
		return nil, nil
	}

	sessions, err := f.Sessions(dir)

	if err != nil {
		return nil, err
	}

	// Each payload is kept once, at the last time it was appended.
	latest := map[string]Entry{}

	for _, session := range sessions {
		entries, err := f.Entries(session.ID)

		if err != nil {
			return nil, err
		}

		for _, entry := range entries {
			if !slices.Contains(kinds, entry.Kind) {
				continue
			}

			payload := string(entry.Payload)

			if known, found := latest[payload]; !found || entry.CreatedAt.After(known.CreatedAt) {
				latest[payload] = Entry{Kind: entry.Kind, Payload: entry.Payload, CreatedAt: entry.CreatedAt}
			}
		}
	}

	recent := make([]Entry, 0, len(latest))

	for _, entry := range latest {
		recent = append(recent, entry)
	}

	slices.SortFunc(recent, func(a, b Entry) int {
		if order := b.CreatedAt.Compare(a.CreatedAt); order != 0 {
			return order
		}

		return strings.Compare(string(a.Payload), string(b.Payload))
	})

	return recent[:min(limit, len(recent))], nil
}

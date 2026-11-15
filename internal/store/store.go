// Package store persists everything rumbo keeps between runs: settings,
// registered programs, sessions, and their entries. Store is the contract;
// SQLite is the implementation.
package store

import (
	"encoding/json"
	"errors"
	"time"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

// ErrLocked is returned when another process is using the session.
var ErrLocked = errors.New("in use by another rumbo process")

// GlobalScope is the settings scope that applies to every project. Any other
// scope is a project directory.
const GlobalScope = ""

// Kinds of registered programs.
const (
	KindProvider = "provider"
	KindFrontend = "frontend"
)

// Program is a registered provider or front end: the command that starts it
// and the configuration handed to it on initialize. Options are the values a
// provider gets with every request, a JSON object or nothing; a front end
// has none.
type Program struct {
	Name    string
	Command []string
	Config  json.RawMessage
	Options json.RawMessage
}

// Session is one conversation. Dir is the directory it works in; Project is
// the root that directory belongs to, which is what sessions are listed and
// continued by. Provider and Model are what the session uses, empty for a
// session from before they were stored. Options are the values of the
// provider's options chosen in the session, a JSON object, or nothing.
// Parent is the session a branch started from, and ParentSeq the last of its
// entries the branch took; a session that is no branch has neither.
type Session struct {
	ID        string
	Dir       string
	Project   string
	Title     string
	Provider  string
	Model     string
	Options   json.RawMessage
	Parent    string
	ParentSeq int64
	CreatedAt time.Time
	// ActiveAt is when the session was last used.
	ActiveAt time.Time
}

// Entry is one immutable record of a session's history. Entries are only ever
// appended, and Seq orders them.
type Entry struct {
	Seq       int64
	Kind      string
	Payload   json.RawMessage
	CreatedAt time.Time
}

// Setting is one stored value, in the scope it was stored for.
type Setting struct {
	Scope string
	Key   string
	Value string
}

// Store is the persistence contract the rest of rumbo depends on.
type Store interface {
	Setting(scope, key string) (string, bool, error)
	SetSetting(scope, key, value string) error
	DeleteSetting(scope, key string) error
	// Settings returns every stored value, in every scope.
	Settings() ([]Setting, error)

	Program(kind, name string) (Program, error)
	PutProgram(kind string, program Program) error
	DeleteProgram(kind, name string) error
	Programs(kind string) ([]Program, error)

	CreateSession(dir, project, title string) (Session, error)
	Session(id string) (Session, error)
	// LatestSession returns the project's most recently used session.
	LatestSession(project string) (Session, error)
	// Sessions lists a project's sessions, most recently used first. An empty
	// project lists the sessions of every project.
	Sessions(project string) ([]Session, error)

	// SetSessionModel records the provider and the model a session uses.
	SetSessionModel(id, provider, model string) error
	// SetSessionOptions records the provider options chosen in a session.
	// Empty options record that none are.
	SetSessionOptions(id string, options json.RawMessage) error

	// BranchSession makes a new session from another one, with the same
	// directory, provider, model, and options, and a copy of its entries up
	// to upTo. The other session is left as it is.
	BranchSession(from string, upTo int64) (Session, error)

	// DeleteSession removes a session and its entries for good.
	DeleteSession(id string) error

	// LockSession gives this process the session until release is called or
	// the process ends. Two processes appending to one session would
	// interleave their entries into a history neither of them sent.
	LockSession(id string) (release func(), err error)

	Append(sessionID, kind string, payload any) (Entry, error)
	Entries(sessionID string) ([]Entry, error)

	Close() error
}

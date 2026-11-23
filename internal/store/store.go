// Package store persists everything rumbo keeps between runs: settings,
// registered programs, sessions, and their entries. Store is the contract;
// Files is the implementation, which keeps all of it in files a person can
// read: what they chose in config.yaml, and each session in a directory of
// its own.
package store

import (
	"encoding/json"
	"errors"
	"time"
)

// ErrNotFound is returned when what was asked for does not exist.
var ErrNotFound = errors.New("not found")

// ErrLocked is returned when another process is using the session.
var ErrLocked = errors.New("in use by another rumbo process")

// Kinds of registered programs.
const (
	KindProvider  = "provider"
	KindFrontend  = "frontend"
	KindExtension = "extension"
)

// Program is a provider, a front end, or an extension as the user registered
// it: the command that starts it, and the configuration the user chose for
// it, a JSON object or nothing. Both may be empty for a program rumbo has built
// in, whose command is rumbo's own. Model is the model a new session starts
// with when it uses a provider, and Replaces the tools of rumbo's own an
// extension takes the place of; no other kind has either.
type Program struct {
	Name     string
	Command  []string
	Model    string
	Replaces []string
	Config   json.RawMessage
}

// Plugin is a plugin as the user added it: Source is where it comes from,
// a directory or the address of a Git repository, and Replaces the tools
// of rumbo's own the user agreed its extensions take the place of.
type Plugin struct {
	Name     string
	Source   string
	Replaces []string
}

// Session is one conversation. Dir is the directory it works in; Project is
// the root that directory belongs to, which is what sessions are listed and
// continued by. Provider and Model are what the session uses. Options are
// the values of the provider's options chosen in the session, a JSON object,
// or nothing. Parent is the session a branch started from, and ParentSeq the
// last of its entries the branch took; a session that is no branch has
// neither.
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

// Store is the persistence contract the rest of rumbo depends on.
type Store interface {
	// Setting returns the value the user chose for a system setting, and
	// whether there is one.
	Setting(key string) (string, bool, error)
	SetSetting(key, value string) error
	DeleteSetting(key string) error
	// Settings returns every system setting the user chose, by key.
	Settings() (map[string]string, error)

	// Program returns what the user registered under a name, which says
	// nothing of the programs rumbo has built in.
	Program(kind, name string) (Program, error)
	PutProgram(kind string, program Program) error
	DeleteProgram(kind, name string) error
	// Programs lists what the user registered, by name.
	Programs(kind string) ([]Program, error)

	// Plugins lists the plugins the user added, by name.
	Plugins() ([]Plugin, error)
	PutPlugin(plugin Plugin) error
	DeletePlugin(name string) error

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
	// RenameSession changes a session's title, which is all a person reads
	// of it in a list.
	RenameSession(id, title string) error

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
	// SessionInUse reports whether a process holds the session, this one
	// included, without taking it.
	SessionInUse(id string) (bool, error)

	Append(sessionID, kind string, payload any) (Entry, error)
	Entries(sessionID string) ([]Entry, error)
	// Recent returns the entries of the kinds given in a project's sessions,
	// most recent first, up to limit. Each payload comes once, at the last
	// time it was appended, so the copies a branch holds do not repeat it;
	// its Seq is not set, as it may be in several sessions.
	Recent(project string, kinds []string, limit int) ([]Entry, error)

	Close() error
}

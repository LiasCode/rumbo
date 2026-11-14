package tool

import (
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
)

// Set is the built-in tools together with what they share for the life of a
// host: which files the model has seen, the processes it left running, and
// the files the tools saved for it.
type Set struct {
	seen  *seen
	jobs  *jobs
	saved *saved
}

// NewSet returns the built-in tools, ready to use.
func NewSet() *Set {
	return &Set{seen: newSeen(), jobs: &jobs{}, saved: &saved{}}
}

// Separate returns a set for another session. It keeps its own record of
// the files its model has seen, since one model has not read what another
// did, and shares the processes left running and the files saved, which go
// with the host.
func (s *Set) Separate() *Set {
	return &Set{seen: newSeen(), jobs: s.jobs, saved: s.saved}
}

// Tools returns the tools in the order they are declared to the model.
func (s *Set) Tools() []Tool {
	return []Tool{Read{seen: s.seen}, Write{seen: s.seen}, Edit{seen: s.seen}, Bash{jobs: s.jobs, saved: s.saved}}
}

// Close stops the processes the model started in the background, and then
// removes the files the tools saved, which those processes wrote to.
func (s *Set) Close() {
	s.jobs.stopAll()
	s.saved.remove()
}

// seen remembers the state of each file as the model last saw it, so a file
// is not replaced whole by a model that does not know what is in it. A nil
// seen checks nothing.
type seen struct {
	mu     sync.Mutex
	stamps map[string]stamp
}

func newSeen() *seen {
	return &seen{stamps: map[string]stamp{}}
}

type stamp struct {
	size     int64
	modified time.Time
}

// record notes that the model knows the current content of path.
func (s *seen) record(path string) {
	if s == nil {
		return
	}

	info, err := os.Stat(path)

	if err != nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.stamps[path] = stamp{size: info.Size(), modified: info.ModTime()}
}

// overwritable reports why path must not be replaced whole: it exists and
// the model has not read it, or it changed since the model did.
func (s *seen) overwritable(path, name string) error {
	if s == nil {
		return nil
	}

	info, err := os.Stat(path)

	if err != nil {
		return nil
	}

	s.mu.Lock()
	known, found := s.stamps[path]
	s.mu.Unlock()

	if !found {
		return fmt.Errorf("%s already exists and you have not read it. Read it first, or change it with edit", name)
	}

	if known != (stamp{size: info.Size(), modified: info.ModTime()}) {
		return fmt.Errorf("%s changed since you last read it. Read it again before replacing it", name)
	}

	return nil
}

// jobs are the process groups started in the background. A nil jobs tracks
// nothing.
type jobs struct {
	mu     sync.Mutex
	groups []int
}

func (j *jobs) add(group int) {
	if j == nil {
		return
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	j.groups = append(j.groups, group)
}

func (j *jobs) stopAll() {
	if j == nil {
		return
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	// A group that already ended is not there to signal, which is fine.
	for _, group := range j.groups {
		_ = syscall.Kill(-group, syscall.SIGKILL)
	}

	j.groups = nil
}

// saved is the directory where the tools keep what they save for the model:
// the whole of a long output, and what a background process writes. It is
// made when first needed and removed with the host, so nothing is left in
// the system's temporary directory. A nil saved creates files there and
// removes nothing.
type saved struct {
	mu  sync.Mutex
	dir string
}

// create makes a new file, named from pattern as os.CreateTemp names it.
func (s *saved) create(pattern string) (*os.File, error) {
	if s == nil {
		return os.CreateTemp("", pattern)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.dir == "" {
		dir, err := os.MkdirTemp("", "rumbo-*")

		if err != nil {
			return nil, err
		}

		s.dir = dir
	}

	return os.CreateTemp(s.dir, pattern)
}

// remove deletes the directory and everything in it.
func (s *saved) remove() {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.dir != "" {
		_ = os.RemoveAll(s.dir)
		s.dir = ""
	}
}

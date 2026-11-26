//go:build windows

package store

import (
	"errors"
	"io/fs"
	"os"
)

// renameSession renames the directory of session id, as a deletion does.
// Windows renames no directory with a file open in it, so the lock this
// process holds on the session is let go of first. That leaves no moment
// for another rumbo to start in it: while one has the session's file open,
// the rename is refused, which is the session being in use.
func (f *Files) renameSession(id, dir, doomed string) error {
	f.mu.Lock()
	file := f.held[id]
	f.mu.Unlock()

	if file != nil {
		f.letGo(id, file)
	}

	err := os.Rename(dir, doomed)

	if errors.Is(err, fs.ErrPermission) {
		return ErrLocked
	}

	return err
}

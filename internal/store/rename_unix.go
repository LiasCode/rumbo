//go:build unix

package store

import "os"

// renameSession renames the directory of session id, as a deletion does.
// A lock this process holds on it stays held meanwhile, so no rumbo starts
// to work in it.
func (f *Files) renameSession(id, dir, doomed string) error {
	return os.Rename(dir, doomed)
}

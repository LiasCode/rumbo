// Package filelock holds locks that the operating system drops when the
// process that holds them ends, however it ends.
package filelock

import "errors"

// ErrHeld is what TryLock returns when another open file holds the lock.
var ErrHeld = errors.New("the lock is held")

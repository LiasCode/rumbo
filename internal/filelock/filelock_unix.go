//go:build unix

package filelock

import (
	"errors"
	"os"
	"syscall"
)

// Lock waits until file is locked for it alone. Closing the file releases
// the lock.
func Lock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}

// TryLock locks file for it alone, or returns ErrHeld at once when another
// open file holds it. Closing the file releases the lock.
func TryLock(file *os.File) error {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)

	if errors.Is(err, syscall.EWOULDBLOCK) {
		return ErrHeld
	}

	return err
}

// Hold runs do while it holds the directory dir, so two processes that read
// a file there to write it back do not lose each other's change.
func Hold(dir string, do func() error) error {
	held, err := os.Open(dir)

	if err != nil {
		return err
	}

	defer held.Close()

	if err := Lock(held); err != nil {
		return err
	}

	return do()
}

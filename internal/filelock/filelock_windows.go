//go:build windows

package filelock

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/windows"
)

// Windows' locks are mandatory: a locked range cannot be written through
// another handle, this process's too. The lock is taken on one byte far
// past anything a file holds, so what is written to the file is not
// stopped by it.
const (
	lockedOffsetLow  = 0xFFFFFFFF
	lockedOffsetHigh = 0x7FFFFFFF
)

// Lock waits until file is locked for it alone. Closing the file releases
// the lock.
func Lock(file *os.File) error {
	return lock(file, windows.LOCKFILE_EXCLUSIVE_LOCK)
}

// TryLock locks file for it alone, or returns ErrHeld at once when another
// open file holds it. Closing the file releases the lock.
func TryLock(file *os.File) error {
	err := lock(file, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY)

	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrHeld
	}

	return err
}

func lock(file *os.File, flags uint32) error {
	overlapped := windows.Overlapped{Offset: lockedOffsetLow, OffsetHigh: lockedOffsetHigh}

	return windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, &overlapped)
}

// Hold runs do while it holds the directory dir, so two processes that read
// a file there to write it back do not lose each other's change. Windows
// cannot lock a directory, so a mutex named after its path stands for it;
// it leaves nothing on the disk.
func Hold(dir string, do func() error) error {
	absolute, err := filepath.Abs(dir)

	if err != nil {
		return err
	}

	sum := sha256.Sum256([]byte(strings.ToLower(absolute)))
	name, err := windows.UTF16PtrFromString(`Local\rumbo-` + hex.EncodeToString(sum[:16]))

	if err != nil {
		return err
	}

	// A mutex belongs to the thread that takes it, so the thread stays the
	// same until it is released.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	mutex, err := windows.CreateMutex(nil, false, name)

	if err != nil {
		return err
	}

	defer windows.CloseHandle(mutex)

	// A mutex left by a process that ended is given to the next one that
	// waits, which is what is wanted.
	event, err := windows.WaitForSingleObject(mutex, windows.INFINITE)

	if err != nil {
		return err
	}

	if event != windows.WAIT_OBJECT_0 && event != windows.WAIT_ABANDONED {
		return errors.New("waiting for the lock of " + dir + " failed")
	}

	defer windows.ReleaseMutex(mutex)

	return do()
}

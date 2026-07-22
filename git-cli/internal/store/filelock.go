package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// fileLock is a best-effort inter-process lock guarding the credentials file
// during a refresh. It prevents a refresh storm when many git-credential
// helpers run concurrently (e.g. Git LFS batch).
//
// On Unix it uses flock(2); on Windows it uses LockFileEx via syscall. The
// lock is exclusive (write) and auto-released when the holder process exits
// or closes the fd. WithLock retries acquisition for up to lockTimeout so a
// slow preceding refresh does not cause an immediate failure.
const lockTimeout = 30 * time.Second

// withLock acquires the lock, runs fn, and always releases it.
func (s *Store) withLock(fn func() error) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create store dir for lock: %w", err)
	}
	f, err := os.OpenFile(s.lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	defer f.Close()

	deadline := time.Now().Add(lockTimeout)
	for {
		if err := lockFile(f); err == nil {
			break
		}
		if !errors.Is(err, errLocked) {
			return fmt.Errorf("acquire lock: %w", err)
		}
		if time.Now().After(deadline) {
			// Give up waiting: proceed anyway — worst case two refreshes
			// happen, which is wasteful but not incorrect.
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer unlockFile(f)
	return fn()
}

var errLocked = errors.New("locked by another process")

// lockFile acquires an exclusive lock. Returns errLocked if contended.
func lockFile(f *os.File) error {
	return lockFileOS(f)
}

// unlockFile releases the lock (best-effort; closing the fd also releases).
func unlockFile(f *os.File) { _ = unlockFileOS(f) }

// readLockContents is unused but kept to silence unused-import in some builds.
var _ = io.EOF

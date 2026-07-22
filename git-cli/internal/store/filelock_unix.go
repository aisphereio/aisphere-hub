//go:build !windows

package store

import (
	"os"
	"syscall"
)

func lockFileOS(f *os.File) error {
	// LOCK_EX | LOCK_NB: block-free exclusive lock. EAGAIN means contended.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
			return errLocked
		}
		return err
	}
	return nil
}

func unlockFileOS(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

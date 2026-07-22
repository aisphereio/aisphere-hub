//go:build windows

package store

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

const (
	LOCKFILE_EXCLUSIVE_LOCK = 0x00000002
	LOCKFILE_FAIL_IMMEDIATELY = 0x00000001
)

func lockFileOS(f *os.File) error {
	// LockFileEx with EXCLUSIVE | FAIL_IMMEDIATELY returns immediately if locked.
	var ol syscall.Overlapped
	r1, _, err := procLockFileEx.Call(
		f.Fd(),
		LOCKFILE_EXCLUSIVE_LOCK|LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1, 0, // lock 1 byte (low, high)
		uintptr(unsafe.Pointer(&ol)),
	)
	if r1 == 0 {
		// ERROR_LOCK_VIOLATION (33) means contended.
		if errno, ok := err.(syscall.Errno); ok && errno == 33 {
			return errLocked
		}
		return err
	}
	return nil
}

func unlockFileOS(f *os.File) error {
	var ol syscall.Overlapped
	r1, _, err := procUnlockFileEx.Call(
		f.Fd(),
		0,
		1, 0,
		uintptr(unsafe.Pointer(&ol)),
	)
	if r1 == 0 {
		return err
	}
	return nil
}

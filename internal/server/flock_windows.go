//go:build windows

package server

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	modkernel32      = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = modkernel32.NewProc("LockFileEx")
	procUnlockFileEx = modkernel32.NewProc("UnlockFileEx")
)

const (
	lockfileExclusiveLock   = 0x2
	lockfileFailImmediately = 0x1
	errorLockViolation      = 33
)

// lockFile: see flock_unix.go. Uses LockFileEx, released when the handle
// closes (including on process exit).
func lockFile(path string, wait bool) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	flags := uintptr(lockfileExclusiveLock)
	if !wait {
		flags |= lockfileFailImmediately
	}
	var ol syscall.Overlapped
	r, _, e := procLockFileEx.Call(f.Fd(), flags, 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
	if r == 0 {
		f.Close()
		if errno, ok := e.(syscall.Errno); ok && errno == errorLockViolation {
			return nil, errLocked
		}
		return nil, e
	}
	return func() {
		var ol syscall.Overlapped
		procUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
		f.Close()
	}, nil
}

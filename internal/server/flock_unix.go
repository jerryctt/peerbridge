//go:build !windows

package server

import (
	"errors"
	"os"
	"syscall"
)

// lockFile takes an exclusive advisory lock on path (created if needed),
// shared by every peerbridge process on the machine. With wait=false it
// fails with errLocked instead of blocking. The lock dies with the process,
// so a crashed server never leaves a stale lock behind.
func lockFile(path string, wait bool) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	how := syscall.LOCK_EX
	if !wait {
		how |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errLocked
		}
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

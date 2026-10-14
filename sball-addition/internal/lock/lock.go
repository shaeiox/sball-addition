// Package lock provides advisory file locking used to guarantee that only one
// watcher runs per node and to serialise updates of the shared runtime files.
package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ErrHeld is returned when the lock is already held by another process.
var ErrHeld = errors.New("lock is held by another process")

// File is an acquired advisory lock backed by flock(2).
type File struct {
	f    *os.File
	path string
}

// Acquire takes an exclusive, non-blocking lock on path. The lock is released
// automatically by the kernel if the process dies, so a crashed watcher never
// leaves a stale lock behind.
func Acquire(path string) (*File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create runtime directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrHeld, path)
		}
		return nil, fmt.Errorf("flock %s: %w", path, err)
	}
	// Record the holder for diagnostics (best effort).
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	l := &File{f: f, path: path}
	return l, nil
}

// Holder returns the pid recorded in a lock file, or 0 when unknown.
func Holder(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}

// Release unlocks and closes the lock file.
func (l *File) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	l.f = nil
	return err
}

// Path returns the lock file path.
func (l *File) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// WithLock runs fn while holding the lock at path.
func WithLock(path string, fn func() error) error {
	l, err := Acquire(path)
	if err != nil {
		return err
	}
	defer func() { _ = l.Release() }()
	return fn()
}

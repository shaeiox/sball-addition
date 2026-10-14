package lock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireAndRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime", "watcher.lock")
	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if l.Path() != path {
		t.Errorf("Path = %q", l.Path())
	}
	if holder := Holder(path); holder != os.Getpid() {
		t.Errorf("Holder = %d, want %d", holder, os.Getpid())
	}

	// A second lock in the same process must fail (flock is per file handle).
	if _, err := Acquire(path); !errors.Is(err, ErrHeld) {
		t.Fatalf("second Acquire err = %v, want ErrHeld", err)
	}

	if err := l.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	l2, err := Acquire(path)
	if err != nil {
		t.Fatalf("re-Acquire after release: %v", err)
	}
	defer func() { _ = l2.Release() }()
}

func TestWithLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	called := false
	if err := WithLock(path, func() error {
		called = true
		return nil
	}); err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	if !called {
		t.Error("the callback was not invoked")
	}
	// The lock must be released when the callback returns.
	if err := WithLock(path, func() error { return nil }); err != nil {
		t.Fatalf("second WithLock: %v", err)
	}
}

func TestWithLockSerialises(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- WithLock(path, func() error {
			close(started)
			<-timeAfter()
			return nil
		})
	}()
	<-started
	if _, err := Acquire(path); !errors.Is(err, ErrHeld) {
		t.Fatalf("err = %v, want ErrHeld while the callback runs", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("WithLock: %v", err)
	}
}

func TestHolderOfMissingFile(t *testing.T) {
	if got := Holder(filepath.Join(t.TempDir(), "nope.lock")); got != 0 {
		t.Errorf("Holder = %d, want 0", got)
	}
}

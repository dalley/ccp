package fslock

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAcquireReleaseRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")

	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// Second acquire after release should succeed immediately.
	l2, err := Acquire(path)
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	if err := l2.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
}

// TestMutualExclusion verifies that a second Acquire blocks until the first
// is released. We can't easily test from a single goroutine because flock is
// per-process; spawn a goroutine instead.
func TestMutualExclusion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")

	l1, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire #1: %v", err)
	}

	var acquired int32
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		l2, err := Acquire(path)
		if err != nil {
			t.Errorf("Acquire #2: %v", err)
			return
		}
		atomic.StoreInt32(&acquired, 1)
		_ = l2.Release()
	}()

	// Give the goroutine time to attempt the lock; it must block.
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&acquired) != 0 {
		t.Fatalf("second Acquire did not block while first held")
	}
	if err := l1.Release(); err != nil {
		t.Fatalf("Release #1: %v", err)
	}
	wg.Wait()
	if atomic.LoadInt32(&acquired) != 1 {
		t.Fatalf("second Acquire never completed after release")
	}
}

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// waitFor polls cond every 10ms until it returns true or the deadline is exceeded.
func waitFor(t *testing.T, desc string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", desc)
}

// TestDiskFault_ExitsWhenFileDeleted verifies that deleting the fill file causes
// the fault goroutine to exit and the file descriptor to be closed (freeing blocks).
func TestDiskFault_ExitsWhenFileDeleted(t *testing.T) {
	dir := t.TempDir()
	fillPath := filepath.Join(dir, ".onfire-diskfill")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		runDiskFault(ctx, map[string]string{"path": dir})
		close(done)
	}()

	// Wait for the fill file to appear, then delete it immediately.
	waitFor(t, "fill file created", 2*time.Second, func() bool {
		_, err := os.Stat(fillPath)
		return err == nil
	})

	if err := os.Remove(fillPath); err != nil {
		t.Fatalf("could not remove fill file: %v", err)
	}

	// Goroutine must exit within 2s (poll interval is 500ms).
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runDiskFault did not exit after fill file was deleted")
	}

	// File must not exist after the goroutine exits.
	if _, err := os.Stat(fillPath); !os.IsNotExist(err) {
		t.Error("fill file still exists after fault stopped")
	}
}

// TestDiskFault_ExitsOnContextCancel verifies that cancelling the context stops
// the fault and cleans up the fill file.
func TestDiskFault_ExitsOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	fillPath := filepath.Join(dir, ".onfire-diskfill")

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		runDiskFault(ctx, map[string]string{"path": dir})
		close(done)
	}()

	// Wait for the fill file to appear before cancelling.
	waitFor(t, "fill file created", 2*time.Second, func() bool {
		_, err := os.Stat(fillPath)
		return err == nil
	})

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runDiskFault did not exit on context cancel")
	}

	// Fill file must be cleaned up by the defer.
	if _, err := os.Stat(fillPath); !os.IsNotExist(err) {
		t.Error("fill file not cleaned up after context cancel")
	}
}

// TODO: test the disk-full branch (write returns error, then file is deleted).
// Requires a small tmpfs or equivalent — needs root and is platform-specific.

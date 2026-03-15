package watcher

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func TestWatcher_DetectsNewFile(t *testing.T) {
	dir := t.TempDir()

	var called atomic.Int32
	var mu sync.Mutex
	var lastDir string

	w, err := New(dir, Config{
		Debounce: 100 * time.Millisecond,
	}, func(ctx context.Context, watchDir string) error {
		called.Add(1)
		mu.Lock()
		lastDir = watchDir
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatalf("create watcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	writeFile(t, dir, "notes.md", "Alice decided to use Go for Acme.")

	time.Sleep(500 * time.Millisecond)
	cancel()
	<-done

	if called.Load() == 0 {
		t.Error("expected callback to be called when new file is created")
	}

	mu.Lock()
	if lastDir != dir {
		t.Errorf("expected callback dir=%q, got %q", dir, lastDir)
	}
	mu.Unlock()
}

func TestWatcher_IgnoresNonTextFiles(t *testing.T) {
	dir := t.TempDir()

	var called atomic.Int32

	w, err := New(dir, Config{
		Debounce: 100 * time.Millisecond,
	}, func(ctx context.Context, watchDir string) error {
		called.Add(1)
		return nil
	})
	if err != nil {
		t.Fatalf("create watcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	writeFile(t, dir, "image.png", "not a real png")
	writeFile(t, dir, "data.json", `{"key": "value"}`)

	time.Sleep(500 * time.Millisecond)
	cancel()
	<-done

	if called.Load() != 0 {
		t.Errorf("expected 0 callbacks for non-text files, got %d", called.Load())
	}
}

func TestWatcher_GracefulShutdown(t *testing.T) {
	dir := t.TempDir()

	w, err := New(dir, Config{
		Debounce: 100 * time.Millisecond,
	}, func(ctx context.Context, watchDir string) error {
		return nil
	})
	if err != nil {
		t.Fatalf("create watcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not stop after context cancel")
	}
}

func TestWatcher_Debounce(t *testing.T) {
	dir := t.TempDir()

	var called atomic.Int32

	w, err := New(dir, Config{
		Debounce: 300 * time.Millisecond,
	}, func(ctx context.Context, watchDir string) error {
		called.Add(1)
		return nil
	})
	if err != nil {
		t.Fatalf("create watcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)

	for i := 0; i < 5; i++ {
		writeFile(t, dir, "notes.md", "Version "+string(rune('A'+i)))
		time.Sleep(50 * time.Millisecond)
	}

	time.Sleep(800 * time.Millisecond)
	cancel()
	<-done

	c := called.Load()
	if c < 1 {
		t.Error("expected at least 1 callback after debounce")
	}
	if c >= 5 {
		t.Errorf("expected debounce to coalesce events, got %d callbacks for 5 writes", c)
	}
}

func TestWatcher_DetectsModifiedFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "notes.md", "Version 1")

	var called atomic.Int32

	w, err := New(dir, Config{
		Debounce: 100 * time.Millisecond,
	}, func(ctx context.Context, watchDir string) error {
		called.Add(1)
		return nil
	})
	if err != nil {
		t.Fatalf("create watcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	writeFile(t, dir, "notes.md", "Version 2 -- completely different content")

	time.Sleep(500 * time.Millisecond)
	cancel()
	<-done

	if called.Load() == 0 {
		t.Error("expected callback when existing file is modified")
	}
}

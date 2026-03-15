// Package watcher monitors a directory for new/modified transcript files
// and triggers processing via a callback.
package watcher

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Config holds watcher settings.
type Config struct {
	Debounce time.Duration
}

// ProcessFunc is called when the watcher detects relevant file changes.
// It receives the watched directory path so the caller can run ProcessDir.
type ProcessFunc func(ctx context.Context, dir string) error

// Watcher monitors a directory using fsnotify and calls the process
// function when .txt or .md files are created or modified.
type Watcher struct {
	dir     string
	cfg     Config
	process ProcessFunc
	logger  *slog.Logger
}

// New creates a Watcher. It does not start watching -- call Run for that.
func New(dir string, cfg Config, process ProcessFunc) (*Watcher, error) {
	if cfg.Debounce == 0 {
		cfg.Debounce = 2 * time.Second
	}
	return &Watcher{
		dir:     dir,
		cfg:     cfg,
		process: process,
		logger:  slog.Default(),
	}, nil
}

func isRelevant(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".txt" || ext == ".md"
}

// Run blocks until ctx is cancelled, watching for file changes.
func (w *Watcher) Run(ctx context.Context) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		w.logger.Error("failed to create fsnotify watcher", "error", err)
		return
	}
	defer fsw.Close()

	if err := fsw.Add(w.dir); err != nil {
		w.logger.Error("failed to watch directory", "dir", w.dir, "error", err)
		return
	}

	w.logger.Info("watcher started", "dir", w.dir, "debounce", w.cfg.Debounce)

	var debounceTimer *time.Timer
	var debounceCh <-chan time.Time

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("watcher stopped")
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return

		case event, ok := <-fsw.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Create|fsnotify.Write) == 0 {
				continue
			}
			if !isRelevant(event.Name) {
				continue
			}

			w.logger.Debug("file event", "op", event.Op.String(), "path", event.Name)

			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.NewTimer(w.cfg.Debounce)
			debounceCh = debounceTimer.C

		case err, ok := <-fsw.Errors:
			if !ok {
				return
			}
			w.logger.Error("fsnotify error", "error", err)

		case <-debounceCh:
			debounceCh = nil
			debounceTimer = nil

			w.logger.Info("processing directory after debounce", "dir", w.dir)
			if err := w.process(ctx, w.dir); err != nil {
				w.logger.Error("process failed", "dir", w.dir, "error", err)
			}
		}
	}
}

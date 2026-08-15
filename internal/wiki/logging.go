package wiki

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// rotatingWriter mirrors the Rust file appender: daily rotation (or
// never), prefix "wiki", suffix "log", pruned to maxFiles.
type rotatingWriter struct {
	mu       sync.Mutex
	dir      string
	rotation string // "daily" | "never"
	maxFiles int

	curDate string
	curFile *os.File
}

func (w *rotatingWriter) targetName() string {
	if w.rotation == "never" {
		return filepath.Join(w.dir, "wiki.log")
	}
	return filepath.Join(w.dir, "wiki-"+time.Now().Format("20060102")+".log")
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	today := time.Now().Format("20060102")
	if w.curFile == nil || (w.rotation == "daily" && w.curDate != today) {
		if w.curFile != nil {
			w.curFile.Close()
		}
		if err := os.MkdirAll(w.dir, 0o755); err != nil {
			return 0, err
		}
		f, err := os.OpenFile(w.targetName(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return 0, err
		}
		w.curFile = f
		w.curDate = today
		w.prune()
	}
	return w.curFile.Write(p)
}

func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.curFile != nil {
		err := w.curFile.Close()
		w.curFile = nil
		return err
	}
	return nil
}

func (w *rotatingWriter) prune() {
	if w.maxFiles <= 0 {
		return
	}
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, "wiki-") && strings.HasSuffix(n, ".log") {
			names = append(names, n)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for i := w.maxFiles; i < len(names); i++ {
		os.Remove(filepath.Join(w.dir, names[i]))
	}
}

// NewServeLogger builds the serve-command logger: file appender under
// logPath (daily rotation, JSON when configured) plus a stderr mirror.
// Empty logPath disables the file layer.
func NewServeLogger(cfg LoggingConfig) *slog.Logger {
	level := slog.LevelInfo
	if v := os.Getenv("LLM_WIKI_LOG_LEVEL"); v != "" {
		switch strings.ToLower(v) {
		case "debug":
			level = slog.LevelDebug
		case "warn", "warning":
			level = slog.LevelWarn
		case "error":
			level = slog.LevelError
		}
	}
	stderr := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	if cfg.LogPath == "" {
		return stderr
	}
	rw := &rotatingWriter{dir: cfg.LogPath, rotation: cfg.LogRotation, maxFiles: cfg.LogMaxFiles}
	var fileHandler slog.Handler
	if cfg.LogFormat == "json" {
		fileHandler = slog.NewJSONHandler(rw, &slog.HandlerOptions{Level: level})
	} else {
		fileHandler = slog.NewTextHandler(rw, &slog.HandlerOptions{Level: level})
	}
	return slog.New(&mirrorHandler{file: fileHandler, fallback: stderr.Handler()})
}

type mirrorHandler struct {
	file     slog.Handler
	fallback slog.Handler
}

func (h *mirrorHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.file.Enabled(ctx, l)
}

func (h *mirrorHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.fallback.Enabled(ctx, r.Level) {
		if err := h.fallback.Handle(ctx, r.Clone()); err != nil {
			return err
		}
	}
	return h.file.Handle(ctx, r)
}

func (h *mirrorHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &mirrorHandler{file: h.file.WithAttrs(attrs), fallback: h.fallback.WithAttrs(attrs)}
}

func (h *mirrorHandler) WithGroup(name string) slog.Handler {
	return &mirrorHandler{file: h.file.WithGroup(name), fallback: h.fallback.WithGroup(name)}
}

var _ io.Writer = (*rotatingWriter)(nil)

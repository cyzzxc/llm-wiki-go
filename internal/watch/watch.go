// Package watch implements filesystem watching with debounce for
// auto-ingest on file save.
package watch

import (
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"llm-wiki-go/internal/wiki"
)

// Event is a debounced change notification for one wiki.
type Event struct {
	Wiki  string
	Paths []string // wiki-relative changed paths
}

// Watcher debounces filesystem events per wiki space and emits ingest
// events. The Rust original debounces 500ms and ingests the changed path.
type Watcher struct {
	Engine     *wiki.WikiEngine
	Debounce   time.Duration
	Log        *slog.Logger
	Events     chan Event
	ShutdownCh chan struct{}
	once       sync.Once
}

// New creates a watcher over all mounted wiki spaces.
func New(engine *wiki.WikiEngine, debounce time.Duration, log *slog.Logger) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if debounce <= 0 {
		debounce = 500 * time.Millisecond
	}
	if log == nil {
		log = slog.Default()
	}
	w := &Watcher{
		Engine:     engine,
		Debounce:   debounce,
		Log:        log,
		Events:     make(chan Event, 64),
		ShutdownCh: make(chan struct{}),
	}
	for _, space := range engine.SpacesList() {
		if err := watchDirRecursive(fw, space.WikiRoot); err != nil {
			log.Warn("watch: cannot watch wiki", "wiki", space.Name, "error", err)
			continue
		}
	}
	go w.loop(fw)
	return w, nil
}

func watchDirRecursive(fw *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return fw.Add(path)
		}
		return nil
	})
}

// pending change aggregation per wiki.
type pendingState struct {
	timer    *time.Timer
	paths    map[string]bool
	wikiname string
}

func (w *Watcher) loop(fw *fsnotify.Watcher) {
	defer fw.Close()
	mu := sync.Mutex{}
	pending := map[string]*pendingState{}
	// map watched dir → wiki name
	dirWiki := map[string]string{}
	for _, space := range w.Engine.SpacesList() {
		dirWiki[space.WikiRoot] = space.Name
	}

	spaceFor := func(path string) string {
		for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
			if name, ok := dirWiki[dir]; ok {
				return name
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
		}
		// fallback: first space whose wiki_root prefixes the path
		for _, space := range w.Engine.SpacesList() {
			if strings.HasPrefix(path, space.WikiRoot) {
				return space.Name
			}
		}
		return ""
	}

	flush := func(state *pendingState) {
		var paths []string
		for p := range state.paths {
			paths = append(paths, p)
		}
		w.Events <- Event{Wiki: state.wikiname, Paths: paths}
	}

	for {
		select {
		case <-w.ShutdownCh:
			return
		case err, ok := <-fw.Errors:
			if !ok {
				return
			}
			w.Log.Warn("watch error", "error", err)
		case ev, ok := <-fw.Events:
			if !ok {
				return
			}
			// Remove/Rename matter too: a deletion must reach the index.
			// spaceFor maps by directory, which still exists for removes.
			wikiName := spaceFor(ev.Name)
			if wikiName == "" {
				continue
			}
			mu.Lock()
			state, ok := pending[wikiName]
			if !ok {
				state = &pendingState{paths: map[string]bool{}, wikiname: wikiName}
				pending[wikiName] = state
			}
			state.paths[ev.Name] = true
			if state.timer != nil {
				state.timer.Stop()
			}
			wn := wikiName
			state.timer = time.AfterFunc(w.Debounce, func() {
				mu.Lock()
				st := pending[wn]
				delete(pending, wn)
				mu.Unlock()
				if st != nil {
					flush(st)
				}
			})
			mu.Unlock()
		}
	}
}

// Stop shuts the watcher down.
func (w *Watcher) Stop() {
	w.once.Do(func() { close(w.ShutdownCh) })
}

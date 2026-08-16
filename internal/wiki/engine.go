package wiki

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	embedpkg "llm-wiki-go/internal/embed"
	"llm-wiki-go/internal/tokenizer"
)

// SpaceContext is the runtime state of one mounted wiki space.
type SpaceContext struct {
	Name         string
	WikiRoot     string
	RepoRoot     string
	TypeRegistry *TypeRegistry
	IndexSchema  *IndexSchema
	IndexManager *IndexManager
	Tokenizer    *tokenizer.Tokenizer
	// Embed is the semantic-search client; nil when [embedding] is off.
	Embed      *embedpkg.Client
	GraphCache *GraphCache
	Resolved   ResolvedConfig

	communityMu       sync.Mutex
	communityGen      string
	communityStats    *CommunityStats
	communityMap      map[string]int
	communityLocalCnt int
}

// EngineState is the engine's shared state under WikiEngine's lock.
type EngineState struct {
	Config     *GlobalConfig
	ConfigPath string
	StateDir   string
	Spaces     map[string]*SpaceContext
}

// WikiEngine owns all mounted wiki spaces.
type WikiEngine struct {
	mu    sync.RWMutex
	State EngineState
}

// BuildEngine loads the global config and mounts every registered wiki.
func BuildEngine(configPath string) (*WikiEngine, error) {
	config, err := LoadGlobal(configPath)
	if err != nil {
		return nil, err
	}
	stateDir := filepath.Dir(configPath)
	if stateDir == "" {
		stateDir = "."
	}
	spaces := map[string]*SpaceContext{}
	for _, entry := range config.Wikis {
		ctx, err := MountSpace(entry, stateDir, config)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to mount wiki %q: %v\n", entry.Name, err)
			continue
		}
		spaces[entry.Name] = ctx
	}
	return &WikiEngine{State: EngineState{
		Config: config, ConfigPath: configPath, StateDir: stateDir, Spaces: spaces,
	}}, nil
}

// DefaultWikiName returns the configured default wiki.
func (e *WikiEngine) DefaultWikiName() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.State.Config.Global.DefaultWiki
}

// Space looks up a mounted space by name.
func (e *WikiEngine) Space(name string) (*SpaceContext, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	s, ok := e.State.Spaces[name]
	if !ok {
		return nil, fmt.Errorf("wiki %q is not mounted", name)
	}
	return s, nil
}

// ResolveWiki returns explicit if mounted, else the default wiki.
func (e *WikiEngine) ResolveWiki(explicit string) (*SpaceContext, error) {
	if explicit == "" {
		explicit = e.DefaultWikiName()
	}
	return e.Space(explicit)
}

// SpacesList returns all mounted spaces sorted by name.
func (e *WikiEngine) SpacesList() []*SpaceContext {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]*SpaceContext, 0, len(e.State.Spaces))
	for _, s := range e.State.Spaces {
		out = append(out, s)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].Name > out[j].Name; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// IndexPathFor returns the per-wiki index directory.
func (e *WikiEngine) IndexPathFor(wikiName string) string {
	return filepath.Join(e.State.StateDir, "indexes", wikiName)
}

// RefreshIndex incrementally updates a space's index from git changes.
func (e *WikiEngine) RefreshIndex(wikiName string) (UpdateReport, error) {
	space, err := e.Space(wikiName)
	if err != nil {
		return UpdateReport{}, err
	}
	return space.IndexManager.Update(space.WikiRoot, space.RepoRoot, space.IndexManager.LastCommit(), space.IndexSchema, space.TypeRegistry)
}

// RebuildIndex rebuilds a space's index from scratch.
func (e *WikiEngine) RebuildIndex(wikiName string) (IndexReport, error) {
	space, err := e.Space(wikiName)
	if err != nil {
		return IndexReport{}, err
	}
	return space.IndexManager.Rebuild(space.WikiRoot, space.RepoRoot, space.IndexSchema, space.TypeRegistry)
}

// SchemaRebuild performs a staleness-aware smart rebuild.
func (e *WikiEngine) SchemaRebuild(wikiName string) error {
	space, err := e.Space(wikiName)
	if err != nil {
		return err
	}
	im := space.IndexManager
	switch im.Staleness(space.RepoRoot, embedModelOf(e.State.Config)) {
	case StalenessCurrent:
		return nil
	case StalenessCommitChanged:
		_, err := im.Update(space.WikiRoot, space.RepoRoot, im.LastCommit(), space.IndexSchema, space.TypeRegistry)
		return err
	case StalenessTypesChanged:
		if err := im.RebuildTypes(im.ChangedTypes(), space.WikiRoot, space.RepoRoot, space.IndexSchema, space.TypeRegistry); err != nil {
			_, err := im.Rebuild(space.WikiRoot, space.RepoRoot, space.IndexSchema, space.TypeRegistry)
			return err
		}
		return nil
	default:
		_, err := im.Rebuild(space.WikiRoot, space.RepoRoot, space.IndexSchema, space.TypeRegistry)
		return err
	}
}

// MountWiki hot-mounts a wiki into the running engine.
func (e *WikiEngine) MountWiki(entry WikiEntry) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	ctx, err := MountSpace(entry, e.State.StateDir, e.State.Config)
	if err != nil {
		return err
	}
	e.State.Spaces[entry.Name] = ctx
	return nil
}

// UnmountWiki removes a mounted wiki (refuses on the default).
func (e *WikiEngine) UnmountWiki(name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.State.Config.Global.DefaultWiki == name {
		return fmt.Errorf("%q is the default wiki — set a new default first", name)
	}
	if _, ok := e.State.Spaces[name]; !ok {
		return fmt.Errorf("wiki %q is not mounted", name)
	}
	delete(e.State.Spaces, name)
	return nil
}

// SetDefault updates the default wiki in the running engine.
func (e *WikiEngine) SetDefault(name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.State.Spaces[name]; !ok {
		return fmt.Errorf("wiki %q is not mounted", name)
	}
	e.State.Config.Global.DefaultWiki = name
	return nil
}

// MountSpace builds a SpaceContext: registry, index, caches, staleness
// handling on first build / auto-rebuild.
func MountSpace(entry WikiEntry, stateDir string, config *GlobalConfig) (*SpaceContext, error) {
	repoRoot := entry.Path
	wikiCfg, err := LoadWiki(repoRoot)
	if err != nil {
		return nil, err
	}
	wikiRoot := filepath.Join(repoRoot, wikiCfg.WikiRoot)
	indexPath := filepath.Join(stateDir, "indexes", entry.Name)

	registry, indexSchema, err := BuildSpace(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to build type registry for wiki %q from %s: %w", entry.Name, filepath.Join(repoRoot, "schemas"), err)
	}

	resolved := Resolve(config, wikiCfg)

	tokName := resolved.Index.Tokenizer
	if tokName == "" {
		tokName = "auto"
	}
	tok, err := tokenizer.New(tokName)
	if err != nil {
		return nil, err
	}

	im := NewIndexManager(entry.Name, indexPath, tok)
	if err := os.MkdirAll(indexPath, 0o755); err != nil {
		return nil, err
	}

	var embedClient *embedpkg.Client
	if config.Embedding.Usable() {
		embedClient = embedpkg.New(config.Embedding)
		im.SetEmbedClient(embedClient)
	}

	status := im.Status(repoRoot)
	needsFirstBuild := status.Built == nil
	if needsFirstBuild {
		im.Rebuild(wikiRoot, repoRoot, indexSchema, registry)
	} else if resolved.Index.AutoRebuild {
		switch im.Staleness(repoRoot, embedModelOf(config)) {
		case StalenessCurrent:
		case StalenessCommitChanged:
			im.Update(wikiRoot, repoRoot, im.LastCommit(), indexSchema, registry)
		case StalenessTypesChanged:
			if err := im.RebuildTypes(im.ChangedTypes(), wikiRoot, repoRoot, indexSchema, registry); err != nil {
				im.Rebuild(wikiRoot, repoRoot, indexSchema, registry)
			}
		default:
			im.Rebuild(wikiRoot, repoRoot, indexSchema, registry)
		}
	}
	im.Open()

	var graphCache *GraphCache
	if resolved.Graph.Snapshot {
		format := resolved.Graph.SnapshotFormat
		compressed := format == "bincode+lz4" || format == "bincode+zstd" || format == "gob+gzip"
		graphCache = NewGraphCache(filepath.Join(stateDir, "snapshots", entry.Name), max(resolved.Graph.SnapshotKeep, 1), compressed)
		if key := graphCache.WarmStart(); key != "" && key != GitCurrentHead(repoRoot) {
			graphCache.Invalidate() // snapshot from another commit — rebuild on demand
		}
	} else {
		graphCache = NewGraphCache("", 0, false)
	}

	return &SpaceContext{
		Name:         entry.Name,
		WikiRoot:     wikiRoot,
		RepoRoot:     repoRoot,
		TypeRegistry: registry,
		IndexSchema:  indexSchema,
		IndexManager: im,
		Tokenizer:    tok,
		Embed:        embedClient,
		GraphCache:   graphCache,
		Resolved:     resolved,
	}, nil
}

func embedModelOf(config *GlobalConfig) string {
	if config.Embedding.Usable() {
		return config.Embedding.Model
	}
	return ""
}

// GetOrBuildGraph returns the cached full graph (bypassing the cache for
// non-default filters).
func (s *SpaceContext) GetOrBuildGraph(filter GraphFilter) (*WikiGraph, error) {
	build := func() (*WikiGraph, error) {
		ix := s.IndexManager.Searcher()
		if ix == nil {
			return NewWikiGraph(), nil
		}
		return BuildGraph(ix, filter, s.TypeRegistry), nil
	}
	if !filter.IsDefault() {
		return build()
	}
	return s.GraphCache.GetFresh(s.IndexManager.LastCommit(), func() (*WikiGraph, error) {
		ix := s.IndexManager.Searcher()
		if ix == nil {
			return NewWikiGraph(), nil
		}
		return BuildGraph(ix, GraphFilter{}, s.TypeRegistry), nil
	})
}

// CommunityData returns the cached community map/stats for the space.
func (s *SpaceContext) CommunityData(minNodes int) (*CommunityStats, map[string]int) {
	s.communityMu.Lock()
	defer s.communityMu.Unlock()
	gen := s.IndexManager.LastCommit()
	if s.communityMap == nil || s.communityGen != gen {
		g, err := s.GraphCache.GetFresh(gen, func() (*WikiGraph, error) {
			ix := s.IndexManager.Searcher()
			if ix == nil {
				return NewWikiGraph(), nil
			}
			return BuildGraph(ix, GraphFilter{}, s.TypeRegistry), nil
		})
		if err != nil {
			return nil, nil
		}
		local := 0
		for _, n := range g.Nodes {
			if !n.External {
				local++
			}
		}
		stats, m := BuildCommunityData(g, 0)
		if stats == nil {
			stats = &CommunityStats{}
			m = map[string]int{}
		}
		s.communityStats, s.communityMap, s.communityGen, s.communityLocalCnt = stats, m, gen, local
	}
	if s.communityLocalCnt < minNodes {
		return nil, nil
	}
	return s.communityStats, s.communityMap
}

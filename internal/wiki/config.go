package wiki

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// GlobalSection is the [global] section of the global config.
type GlobalSection struct {
	DefaultWiki string `toml:"default_wiki"`
}

// WikiEntry is a registered wiki in the [[wikis]] array.
type WikiEntry struct {
	Name        string  `toml:"name"`
	Path        string  `toml:"path"`
	Description *string `toml:"description,omitempty"`
	Remote      *string `toml:"remote,omitempty"`
}

// Defaults are CLI flag defaults overridable per-wiki.
type Defaults struct {
	SearchTopK     int    `toml:"search_top_k"`
	SearchExcerpt  bool   `toml:"search_excerpt"`
	SearchSections bool   `toml:"search_sections"`
	PageMode       string `toml:"page_mode"`
	ListPageSize   int    `toml:"list_page_size"`
	OutputFormat   string `toml:"output_format"`
	FacetsTopTags  int    `toml:"facets_top_tags"`
}

func defaultDefaults() Defaults {
	return Defaults{
		SearchTopK:     10,
		SearchExcerpt:  true,
		SearchSections: false,
		PageMode:       "flat",
		ListPageSize:   20,
		OutputFormat:   "text",
		FacetsTopTags:  10,
	}
}

// ReadConfig is the [read] section.
type ReadConfig struct {
	NoFrontmatter bool `toml:"no_frontmatter"`
}

// IndexConfig is the [index] section. Tokenizer selects the text
// tokenization strategy for the BM25 index: "auto" (default; gse
// dictionary segmentation for Chinese, word tokenizer for Latin),
// "gse"/"zh"/"cjk" (eager gse), or "simple"/"en_stem" (no dictionary).
type IndexConfig struct {
	AutoRebuild  bool   `toml:"auto_rebuild"`
	AutoRecovery bool   `toml:"auto_recovery"`
	MemoryBudget int    `toml:"memory_budget_mb"`
	Tokenizer    string `toml:"tokenizer"`
	DictPath     string `toml:"dict_path"`
}

func defaultIndexConfig() IndexConfig {
	return IndexConfig{AutoRecovery: true, MemoryBudget: 50, Tokenizer: "auto"}
}

// GraphConfig configures graph rendering and community detection.
// SnapshotFormat accepts "bincode", "bincode+lz4", "bincode+zstd"
// (Rust-compat names) and "gob", "gob+gzip"; the Go engine stores
// snapshots as gob, gzip-compressed for every compressed name.
type GraphConfig struct {
	Format                    string   `toml:"format"`
	Depth                     int      `toml:"depth"`
	Type                      []string `toml:"type"`
	Output                    string   `toml:"output"`
	MinNodesForCommunities    int      `toml:"min_nodes_for_communities"`
	CommunitySuggestionsLimit int      `toml:"community_suggestions_limit"`
	Snapshot                  bool     `toml:"snapshot"`
	SnapshotKeep              int      `toml:"snapshot_keep"`
	SnapshotFormat            string   `toml:"snapshot_format"`
	StructuralAlgorithms      bool     `toml:"structural_algorithms"`
	MaxNodesForDiameter       int      `toml:"max_nodes_for_diameter"`
}

func defaultGraphConfig() GraphConfig {
	return GraphConfig{
		Format:                    "mermaid",
		Depth:                     3,
		MinNodesForCommunities:    30,
		CommunitySuggestionsLimit: 2,
		Snapshot:                  true,
		SnapshotKeep:              3,
		SnapshotFormat:            "bincode+lz4",
		StructuralAlgorithms:      true,
		MaxNodesForDiameter:       2000,
	}
}

// ServeConfig is the [serve] section.
type ServeConfig struct {
	HTTP             bool     `toml:"http"`
	HTTPPort         int      `toml:"http_port"`
	HTTPAllowedHosts []string `toml:"http_allowed_hosts"`
	ACP              bool     `toml:"acp"`
	MaxRestarts      int      `toml:"max_restarts"`
	RestartBackoff   int      `toml:"restart_backoff"`
	HeartbeatSecs    int      `toml:"heartbeat_secs"`
	ACPMaxSessions   int      `toml:"acp_max_sessions"`
}

func defaultServeConfig() ServeConfig {
	return ServeConfig{
		HTTPPort:         8080,
		HTTPAllowedHosts: []string{"localhost", "127.0.0.1", "::1"},
		MaxRestarts:      10,
		RestartBackoff:   1,
		HeartbeatSecs:    60,
		ACPMaxSessions:   20,
	}
}

// ValidationConfig is the [validation] section.
type ValidationConfig struct {
	TypeStrictness string `toml:"type_strictness"`
}

func defaultValidationConfig() ValidationConfig { return ValidationConfig{TypeStrictness: "loose"} }

// LoggingConfig is the [logging] section.
type LoggingConfig struct {
	LogPath     string `toml:"log_path"`
	LogRotation string `toml:"log_rotation"`
	LogMaxFiles int    `toml:"log_max_files"`
	LogFormat   string `toml:"log_format"`
}

func defaultLoggingConfig() LoggingConfig {
	home, _ := os.UserHomeDir()
	return LoggingConfig{
		LogPath:     filepath.Join(home, ".llm-wiki", "logs"),
		LogRotation: "daily",
		LogMaxFiles: 7,
		LogFormat:   "text",
	}
}

// IngestConfig is the [ingest] section.
type IngestConfig struct {
	AutoCommit bool `toml:"auto_commit"`
}

func defaultIngestConfig() IngestConfig { return IngestConfig{AutoCommit: true} }

// HistoryConfig is the [history] section.
type HistoryConfig struct {
	Follow       bool `toml:"follow"`
	DefaultLimit int  `toml:"default_limit"`
}

func defaultHistoryConfig() HistoryConfig { return HistoryConfig{Follow: true, DefaultLimit: 10} }

// WatchConfig is the [watch] section.
type WatchConfig struct {
	DebounceMs int `toml:"debounce_ms"`
}

func defaultWatchConfig() WatchConfig { return WatchConfig{DebounceMs: 500} }

// SuggestConfig is the [suggest] section.
type SuggestConfig struct {
	DefaultLimit int     `toml:"default_limit"`
	MinScore     float64 `toml:"min_score"`
}

func defaultSuggestConfig() SuggestConfig { return SuggestConfig{DefaultLimit: 5, MinScore: 0.1} }

// SearchConfig holds BM25 score multipliers by page status.
type SearchConfig struct {
	Status map[string]float64 `toml:"-"`
}

func defaultSearchConfig() SearchConfig {
	return SearchConfig{Status: map[string]float64{
		"active": 1.0, "draft": 0.8, "archived": 0.3, "unknown": 0.9,
	}}
}

// LintConfig configures the stale lint rule.
type LintConfig struct {
	StaleDays                int     `toml:"stale_days"`
	StaleConfidenceThreshold float64 `toml:"stale_confidence_threshold"`
}

func defaultLintConfig() LintConfig {
	return LintConfig{StaleDays: 90, StaleConfidenceThreshold: 0.4}
}

// CustomPattern is a user-defined redaction rule.
type CustomPattern struct {
	Name        string `toml:"name"`
	Pattern     string `toml:"pattern"`
	Replacement string `toml:"replacement"`
}

// RedactConfig is the [redact] section.
type RedactConfig struct {
	Disable  []string        `toml:"disable"`
	Patterns []CustomPattern `toml:"patterns"`
}

// GlobalConfig is the root of ~/.llm-wiki/config.toml.
type GlobalConfig struct {
	Global     GlobalSection    `toml:"global"`
	Wikis      []WikiEntry      `toml:"wikis"`
	Defaults   Defaults         `toml:"defaults"`
	Read       ReadConfig       `toml:"read"`
	Index      IndexConfig      `toml:"index"`
	Graph      GraphConfig      `toml:"graph"`
	Serve      ServeConfig      `toml:"serve"`
	Validation ValidationConfig `toml:"validation"`
	Ingest     IngestConfig     `toml:"ingest"`
	History    HistoryConfig    `toml:"history"`
	Suggest    SuggestConfig    `toml:"suggest"`
	Search     SearchConfig     `toml:"search"`
	Lint       LintConfig       `toml:"lint"`
	Logging    LoggingConfig    `toml:"logging"`
	Watch      WatchConfig      `toml:"watch"`
	Redact     RedactConfig     `toml:"redact"`
}

// NewDefaultGlobalConfig returns the global config defaults.
func NewDefaultGlobalConfig() *GlobalConfig {
	return &GlobalConfig{
		Defaults:   defaultDefaults(),
		Index:      defaultIndexConfig(),
		Graph:      defaultGraphConfig(),
		Serve:      defaultServeConfig(),
		Validation: defaultValidationConfig(),
		Logging:    defaultLoggingConfig(),
		Ingest:     defaultIngestConfig(),
		History:    defaultHistoryConfig(),
		Watch:      defaultWatchConfig(),
		Suggest:    defaultSuggestConfig(),
		Search:     defaultSearchConfig(),
		Lint:       defaultLintConfig(),
	}
}

// TypeEntry is a [types.<name>] registration in wiki.toml.
type TypeEntry struct {
	Schema      string `toml:"schema"`
	Description string `toml:"description"`
}

// WikiConfig is the per-wiki wiki.toml overlay.
type WikiConfig struct {
	Name        string               `toml:"name"`
	Description string               `toml:"description"`
	Types       map[string]TypeEntry `toml:"types"`
	Defaults    *Defaults            `toml:"defaults"`
	Read        *ReadConfig          `toml:"read"`
	Validation  *ValidationConfig    `toml:"validation"`
	Ingest      *IngestConfig        `toml:"ingest"`
	Graph       *GraphConfig         `toml:"graph"`
	History     *HistoryConfig       `toml:"history"`
	Suggest     *SuggestConfig       `toml:"suggest"`
	Search      *SearchConfig        `toml:"search"`
	Lint        *LintConfig          `toml:"lint"`
	Redact      *RedactConfig        `toml:"redact"`
	WikiRoot    string               `toml:"wiki_root"`
}

// ResolvedConfig is the merged configuration for a specific wiki.
type ResolvedConfig struct {
	Defaults   Defaults
	Read       ReadConfig
	Index      IndexConfig
	Graph      GraphConfig
	Serve      ServeConfig
	Ingest     IngestConfig
	Validation ValidationConfig
	History    HistoryConfig
	Suggest    SuggestConfig
	Search     SearchConfig
	Lint       LintConfig
	Redact     RedactConfig
}

// Resolve merges global and per-wiki config for one wiki.
func Resolve(global *GlobalConfig, perWiki *WikiConfig) ResolvedConfig {
	rc := ResolvedConfig{
		Defaults:   global.Defaults,
		Read:       global.Read,
		Index:      global.Index,
		Graph:      global.Graph,
		Serve:      global.Serve,
		Ingest:     global.Ingest,
		Validation: global.Validation,
		History:    global.History,
		Suggest:    global.Suggest,
		Search:     SearchConfig{Status: mapsClone(global.Search.Status)},
		Lint:       global.Lint,
		Redact:     global.Redact,
	}
	if perWiki == nil {
		return rc
	}
	if perWiki.Defaults != nil {
		rc.Defaults = *perWiki.Defaults
	}
	if perWiki.Read != nil {
		rc.Read = *perWiki.Read
	}
	if perWiki.Graph != nil {
		rc.Graph = *perWiki.Graph
	}
	if perWiki.Ingest != nil {
		rc.Ingest = *perWiki.Ingest
	}
	if perWiki.Validation != nil {
		rc.Validation = *perWiki.Validation
	}
	if perWiki.History != nil {
		rc.History = *perWiki.History
	}
	if perWiki.Suggest != nil {
		rc.Suggest = *perWiki.Suggest
	}
	if perWiki.Lint != nil {
		rc.Lint = *perWiki.Lint
	}
	if perWiki.Redact != nil {
		rc.Redact = *perWiki.Redact
	}
	if perWiki.Search != nil {
		for k, v := range perWiki.Search.Status {
			rc.Search.Status[k] = v
		}
	}
	return rc
}

func mapsClone(m map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// searchStatusTOML models the [search.status] table for TOML (de)encoding.
type searchStatusFile struct {
	Search struct {
		Status map[string]float64 `toml:"status"`
	} `toml:"search"`
}

// LoadGlobal loads the global config; missing file yields defaults.
func LoadGlobal(path string) (*GlobalConfig, error) {
	cfg := NewDefaultGlobalConfig()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}
	// decode pointer sections over defaults so partial files merge cleanly
	type fileCfg struct {
		Global     *GlobalSection    `toml:"global"`
		Wikis      []WikiEntry       `toml:"wikis"`
		Defaults   *Defaults         `toml:"defaults"`
		Read       *ReadConfig       `toml:"read"`
		Index      *IndexConfig      `toml:"index"`
		Graph      *GraphConfig      `toml:"graph"`
		Serve      *ServeConfig      `toml:"serve"`
		Validation *ValidationConfig `toml:"validation"`
		Ingest     *IngestConfig     `toml:"ingest"`
		History    *HistoryConfig    `toml:"history"`
		Suggest    *SuggestConfig    `toml:"suggest"`
		Lint       *LintConfig       `toml:"lint"`
		Logging    *LoggingConfig    `toml:"logging"`
		Watch      *WatchConfig      `toml:"watch"`
		Redact     *RedactConfig     `toml:"redact"`
		Search     *searchStatusFile `toml:"search"`
	}
	var f fileCfg
	if err := toml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", path, err)
	}
	if f.Global != nil {
		cfg.Global = *f.Global
	}
	cfg.Wikis = f.Wikis
	if f.Defaults != nil {
		cfg.Defaults = *f.Defaults
	}
	if f.Read != nil {
		cfg.Read = *f.Read
	}
	if f.Index != nil {
		cfg.Index = *f.Index
	}
	if f.Graph != nil {
		cfg.Graph = *f.Graph
	}
	if f.Serve != nil {
		cfg.Serve = *f.Serve
	}
	if f.Validation != nil {
		cfg.Validation = *f.Validation
	}
	if f.Ingest != nil {
		cfg.Ingest = *f.Ingest
	}
	if f.History != nil {
		cfg.History = *f.History
	}
	if f.Suggest != nil {
		cfg.Suggest = *f.Suggest
	}
	if f.Lint != nil {
		cfg.Lint = *f.Lint
	}
	if f.Logging != nil {
		cfg.Logging = *f.Logging
	}
	if f.Watch != nil {
		cfg.Watch = *f.Watch
	}
	if f.Redact != nil {
		cfg.Redact = *f.Redact
	}
	if f.Search != nil && f.Search.Search.Status != nil {
		for k, v := range f.Search.Search.Status {
			cfg.Search.Status[k] = v
		}
	}
	return cfg, nil
}

// SaveGlobal writes the global config to disk.
func SaveGlobal(cfg *GlobalConfig, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buf strings.Builder
	enc := toml.NewEncoder(&buf)
	type fullFile struct {
		Global     GlobalSection    `toml:"global"`
		Wikis      []WikiEntry      `toml:"wikis"`
		Defaults   Defaults         `toml:"defaults"`
		Read       ReadConfig       `toml:"read"`
		Index      IndexConfig      `toml:"index"`
		Graph      GraphConfig      `toml:"graph"`
		Serve      ServeConfig      `toml:"serve"`
		Validation ValidationConfig `toml:"validation"`
		Ingest     IngestConfig     `toml:"ingest"`
		History    HistoryConfig    `toml:"history"`
		Suggest    SuggestConfig    `toml:"suggest"`
		Search     searchStatusFile `toml:"search"`
		Lint       LintConfig       `toml:"lint"`
		Logging    LoggingConfig    `toml:"logging"`
		Watch      WatchConfig      `toml:"watch"`
		Redact     RedactConfig     `toml:"redact"`
	}
	f := fullFile{
		Global: cfg.Global, Wikis: cfg.Wikis, Defaults: cfg.Defaults,
		Read: cfg.Read, Index: cfg.Index, Graph: cfg.Graph, Serve: cfg.Serve,
		Validation: cfg.Validation, Ingest: cfg.Ingest, History: cfg.History,
		Suggest: cfg.Suggest, Lint: cfg.Lint, Logging: cfg.Logging,
		Watch: cfg.Watch, Redact: cfg.Redact,
	}
	f.Search.Search.Status = cfg.Search.Status
	if err := enc.Encode(f); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(buf.String()), 0o644)
}

// LoadWiki loads per-wiki wiki.toml; missing file yields an empty config.
func LoadWiki(repoRoot string) (*WikiConfig, error) {
	path := filepath.Join(repoRoot, "wiki.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &WikiConfig{WikiRoot: "wiki"}, nil
		}
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}
	cfg := &WikiConfig{}
	if err := toml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", path, err)
	}
	var search searchStatusFile
	_ = toml.Unmarshal(raw, &search)
	if search.Search.Status != nil {
		cfg.Search = &SearchConfig{Status: search.Search.Status}
	}
	if cfg.WikiRoot == "" {
		cfg.WikiRoot = "wiki"
	}
	return cfg, nil
}

// SaveWiki writes the per-wiki wiki.toml.
func SaveWiki(cfg *WikiConfig, repoRoot string) error {
	var buf strings.Builder
	enc := toml.NewEncoder(&buf)
	if err := enc.Encode(cfg); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(repoRoot, "wiki.toml"), []byte(buf.String()), 0o644)
}

// ConfigValuePath resolves the config file location:
// --config flag → $LLM_WIKI_CONFIG → ~/.llm-wiki/config.toml.
func ConfigValuePath(flagPath string) string {
	if flagPath != "" {
		return flagPath
	}
	if env := os.Getenv("LLM_WIKI_CONFIG"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, ".llm-wiki", "config.toml")
}

// searchStatusKey extracts the status name from a "search.status.<status>" key.
func searchStatusKey(key string) (string, bool) {
	rest, ok := strings.CutPrefix(key, "search.status.")
	return rest, ok && rest != ""
}

// SetGlobalConfigValue sets a dot-notation key on the global config.
func SetGlobalConfigValue(g *GlobalConfig, key, value string) error {
	parseInt := func() (int, error) { return strconv.Atoi(value) }
	parseFloat := func() (float64, error) { return strconv.ParseFloat(value, 64) }
	_ = parseFloat
	switch key {
	case "global.default_wiki":
		g.Global.DefaultWiki = value
	case "defaults.search_top_k":
		return applyInt(&g.Defaults.SearchTopK, parseInt)
	case "defaults.search_excerpt":
		return applyBool(&g.Defaults.SearchExcerpt, value)
	case "defaults.search_sections":
		return applyBool(&g.Defaults.SearchSections, value)
	case "defaults.page_mode":
		g.Defaults.PageMode = value
	case "defaults.list_page_size":
		return applyInt(&g.Defaults.ListPageSize, parseInt)
	case "defaults.output_format":
		g.Defaults.OutputFormat = value
	case "defaults.facets_top_tags":
		return applyInt(&g.Defaults.FacetsTopTags, parseInt)
	case "read.no_frontmatter":
		return applyBool(&g.Read.NoFrontmatter, value)
	case "index.auto_rebuild":
		return applyBool(&g.Index.AutoRebuild, value)
	case "index.auto_recovery":
		return applyBool(&g.Index.AutoRecovery, value)
	case "index.memory_budget_mb":
		return applyInt(&g.Index.MemoryBudget, parseInt)
	case "index.tokenizer":
		g.Index.Tokenizer = value
	case "graph.format":
		g.Graph.Format = value
	case "graph.depth":
		return applyInt(&g.Graph.Depth, parseInt)
	case "graph.output":
		g.Graph.Output = value
	case "graph.snapshot":
		return applyBool(&g.Graph.Snapshot, value)
	case "graph.snapshot_keep":
		return applyInt(&g.Graph.SnapshotKeep, parseInt)
	case "graph.snapshot_format":
		g.Graph.SnapshotFormat = value
	case "graph.structural_algorithms":
		return applyBool(&g.Graph.StructuralAlgorithms, value)
	case "graph.max_nodes_for_diameter":
		return applyInt(&g.Graph.MaxNodesForDiameter, parseInt)
	case "serve.http":
		return applyBool(&g.Serve.HTTP, value)
	case "serve.http_port":
		return applyInt(&g.Serve.HTTPPort, parseInt)
	case "serve.http_allowed_hosts":
		g.Serve.HTTPAllowedHosts = splitComma(value)
	case "serve.acp":
		return applyBool(&g.Serve.ACP, value)
	case "serve.max_restarts":
		return applyInt(&g.Serve.MaxRestarts, parseInt)
	case "serve.restart_backoff":
		return applyInt(&g.Serve.RestartBackoff, parseInt)
	case "serve.heartbeat_secs":
		return applyInt(&g.Serve.HeartbeatSecs, parseInt)
	case "serve.acp_max_sessions":
		return applyInt(&g.Serve.ACPMaxSessions, parseInt)
	case "ingest.auto_commit":
		return applyBool(&g.Ingest.AutoCommit, value)
	case "history.follow":
		return applyBool(&g.History.Follow, value)
	case "history.default_limit":
		return applyInt(&g.History.DefaultLimit, parseInt)
	case "suggest.default_limit":
		return applyInt(&g.Suggest.DefaultLimit, parseInt)
	case "suggest.min_score":
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("invalid value %q for %s: %v", value, key, err)
		}
		g.Suggest.MinScore = f
	case "validation.type_strictness":
		g.Validation.TypeStrictness = value
	case "logging.log_path":
		g.Logging.LogPath = value
	case "logging.log_rotation":
		g.Logging.LogRotation = value
	case "logging.log_max_files":
		return applyInt(&g.Logging.LogMaxFiles, parseInt)
	case "logging.log_format":
		g.Logging.LogFormat = value
	case "watch.debounce_ms":
		return applyInt(&g.Watch.DebounceMs, parseInt)
	default:
		if status, ok := searchStatusKey(key); ok {
			f, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return fmt.Errorf("invalid value %q for %s: %v", value, key, err)
			}
			g.Search.Status[status] = f
			return nil
		}
		return fmt.Errorf("unknown key: %s", key)
	}
	return nil
}

// GetConfigValue reads a dot-notation key from the resolved config.
func GetConfigValue(resolved *ResolvedConfig, global *GlobalConfig, key string) string {
	switch key {
	case "global.default_wiki":
		return global.Global.DefaultWiki
	case "defaults.search_top_k":
		return strconv.Itoa(resolved.Defaults.SearchTopK)
	case "defaults.search_excerpt":
		return strconv.FormatBool(resolved.Defaults.SearchExcerpt)
	case "defaults.search_sections":
		return strconv.FormatBool(resolved.Defaults.SearchSections)
	case "defaults.page_mode":
		return resolved.Defaults.PageMode
	case "defaults.list_page_size":
		return strconv.Itoa(resolved.Defaults.ListPageSize)
	case "defaults.output_format":
		return resolved.Defaults.OutputFormat
	case "defaults.facets_top_tags":
		return strconv.Itoa(resolved.Defaults.FacetsTopTags)
	case "read.no_frontmatter":
		return strconv.FormatBool(resolved.Read.NoFrontmatter)
	case "index.auto_rebuild":
		return strconv.FormatBool(resolved.Index.AutoRebuild)
	case "index.auto_recovery":
		return strconv.FormatBool(global.Index.AutoRecovery)
	case "index.memory_budget_mb":
		return strconv.Itoa(global.Index.MemoryBudget)
	case "index.tokenizer":
		return resolved.Index.Tokenizer
	case "graph.format":
		return resolved.Graph.Format
	case "graph.depth":
		return strconv.Itoa(resolved.Graph.Depth)
	case "graph.output":
		return resolved.Graph.Output
	case "graph.snapshot":
		return strconv.FormatBool(resolved.Graph.Snapshot)
	case "graph.snapshot_keep":
		return strconv.Itoa(resolved.Graph.SnapshotKeep)
	case "graph.snapshot_format":
		return resolved.Graph.SnapshotFormat
	case "graph.structural_algorithms":
		return strconv.FormatBool(resolved.Graph.StructuralAlgorithms)
	case "graph.max_nodes_for_diameter":
		return strconv.Itoa(resolved.Graph.MaxNodesForDiameter)
	case "serve.http":
		return strconv.FormatBool(resolved.Serve.HTTP)
	case "serve.http_port":
		return strconv.Itoa(resolved.Serve.HTTPPort)
	case "serve.http_allowed_hosts":
		return strings.Join(resolved.Serve.HTTPAllowedHosts, ",")
	case "serve.acp":
		return strconv.FormatBool(resolved.Serve.ACP)
	case "serve.max_restarts":
		return strconv.Itoa(global.Serve.MaxRestarts)
	case "serve.restart_backoff":
		return strconv.Itoa(global.Serve.RestartBackoff)
	case "serve.heartbeat_secs":
		return strconv.Itoa(global.Serve.HeartbeatSecs)
	case "serve.acp_max_sessions":
		return strconv.Itoa(global.Serve.ACPMaxSessions)
	case "validation.type_strictness":
		return resolved.Validation.TypeStrictness
	case "logging.log_path":
		return global.Logging.LogPath
	case "logging.log_rotation":
		return global.Logging.LogRotation
	case "logging.log_max_files":
		return strconv.Itoa(global.Logging.LogMaxFiles)
	case "logging.log_format":
		return global.Logging.LogFormat
	case "watch.debounce_ms":
		return strconv.Itoa(global.Watch.DebounceMs)
	case "ingest.auto_commit":
		return strconv.FormatBool(resolved.Ingest.AutoCommit)
	case "history.follow":
		return strconv.FormatBool(resolved.History.Follow)
	case "history.default_limit":
		return strconv.Itoa(resolved.History.DefaultLimit)
	case "suggest.default_limit":
		return strconv.Itoa(resolved.Suggest.DefaultLimit)
	case "suggest.min_score":
		return strconv.FormatFloat(resolved.Suggest.MinScore, 'f', -1, 64)
	default:
		if status, ok := searchStatusKey(key); ok {
			if mult, ok := resolved.Search.Status[status]; ok {
				return strconv.FormatFloat(mult, 'f', -1, 64)
			}
		}
		return fmt.Sprintf("unknown key: %s", key)
	}
}

// SetWikiConfigValue sets a dot-notation key on a per-wiki config,
// rejecting global-only keys.
func SetWikiConfigValue(w *WikiConfig, key, value string) error {
	parseInt := func() (int, error) { return strconv.Atoi(value) }
	switch key {
	case "defaults.search_top_k":
		return withDefaults(w, func(d *Defaults) error { return applyInt(&d.SearchTopK, parseInt) })
	case "defaults.search_excerpt":
		return withDefaults(w, func(d *Defaults) error { return applyBool(&d.SearchExcerpt, value) })
	case "defaults.search_sections":
		return withDefaults(w, func(d *Defaults) error { return applyBool(&d.SearchSections, value) })
	case "defaults.page_mode":
		return withDefaults(w, func(d *Defaults) error { d.PageMode = value; return nil })
	case "defaults.list_page_size":
		return withDefaults(w, func(d *Defaults) error { return applyInt(&d.ListPageSize, parseInt) })
	case "defaults.output_format":
		return withDefaults(w, func(d *Defaults) error { d.OutputFormat = value; return nil })
	case "defaults.facets_top_tags":
		return withDefaults(w, func(d *Defaults) error { return applyInt(&d.FacetsTopTags, parseInt) })
	case "read.no_frontmatter":
		return withRead(w, func(r *ReadConfig) error { return applyBool(&r.NoFrontmatter, value) })
	case "validation.type_strictness":
		return withValidation(w, func(v *ValidationConfig) error { v.TypeStrictness = value; return nil })
	case "ingest.auto_commit":
		return withIngest(w, func(i *IngestConfig) error { return applyBool(&i.AutoCommit, value) })
	case "history.follow":
		return withHistory(w, func(h *HistoryConfig) error { return applyBool(&h.Follow, value) })
	case "history.default_limit":
		return withHistory(w, func(h *HistoryConfig) error { return applyInt(&h.DefaultLimit, parseInt) })
	case "suggest.default_limit":
		return withSuggest(w, func(s *SuggestConfig) error { return applyInt(&s.DefaultLimit, parseInt) })
	case "suggest.min_score":
		return withSuggest(w, func(s *SuggestConfig) error {
			f, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return fmt.Errorf("invalid value %q for %s: %v", value, key, err)
			}
			s.MinScore = f
			return nil
		})
	case "graph.format":
		return withGraph(w, func(g *GraphConfig) error { g.Format = value; return nil })
	case "graph.depth":
		return withGraph(w, func(g *GraphConfig) error { return applyInt(&g.Depth, parseInt) })
	case "graph.output":
		return withGraph(w, func(g *GraphConfig) error { g.Output = value; return nil })
	case "graph.snapshot":
		return withGraph(w, func(g *GraphConfig) error { return applyBool(&g.Snapshot, value) })
	case "graph.snapshot_keep":
		return withGraph(w, func(g *GraphConfig) error { return applyInt(&g.SnapshotKeep, parseInt) })
	case "graph.snapshot_format":
		return withGraph(w, func(g *GraphConfig) error { g.SnapshotFormat = value; return nil })
	case "graph.structural_algorithms":
		return withGraph(w, func(g *GraphConfig) error { return applyBool(&g.StructuralAlgorithms, value) })
	case "graph.max_nodes_for_diameter":
		return withGraph(w, func(g *GraphConfig) error { return applyInt(&g.MaxNodesForDiameter, parseInt) })
	case "global.default_wiki", "index.auto_rebuild", "index.auto_recovery",
		"index.memory_budget_mb", "index.tokenizer", "serve.http", "serve.http_port",
		"serve.http_allowed_hosts", "serve.acp", "serve.max_restarts", "serve.restart_backoff",
		"serve.heartbeat_secs", "serve.acp_max_sessions", "logging.log_path",
		"logging.log_rotation", "logging.log_max_files", "logging.log_format", "watch.debounce_ms":
		return fmt.Errorf("%s is a global-only key — use --global", key)
	default:
		if status, ok := searchStatusKey(key); ok {
			f, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return fmt.Errorf("invalid value %q for %s: %v", value, key, err)
			}
			if w.Search == nil {
				w.Search = &SearchConfig{Status: map[string]float64{}}
			}
			w.Search.Status[status] = f
			return nil
		}
		return fmt.Errorf("unknown key: %s", key)
	}
}

func withDefaults(w *WikiConfig, f func(*Defaults) error) error {
	if w.Defaults == nil {
		w.Defaults = &Defaults{
			SearchTopK: 10, SearchExcerpt: true, PageMode: "flat",
			ListPageSize: 20, OutputFormat: "text", FacetsTopTags: 10,
		}
	}
	return f(w.Defaults)
}
func withRead(w *WikiConfig, f func(*ReadConfig) error) error {
	if w.Read == nil {
		w.Read = &ReadConfig{}
	}
	return f(w.Read)
}
func withValidation(w *WikiConfig, f func(*ValidationConfig) error) error {
	if w.Validation == nil {
		w.Validation = &ValidationConfig{TypeStrictness: "loose"}
	}
	return f(w.Validation)
}
func withIngest(w *WikiConfig, f func(*IngestConfig) error) error {
	if w.Ingest == nil {
		w.Ingest = &IngestConfig{AutoCommit: true}
	}
	return f(w.Ingest)
}
func withHistory(w *WikiConfig, f func(*HistoryConfig) error) error {
	if w.History == nil {
		w.History = &HistoryConfig{Follow: true, DefaultLimit: 10}
	}
	return f(w.History)
}
func withSuggest(w *WikiConfig, f func(*SuggestConfig) error) error {
	if w.Suggest == nil {
		w.Suggest = &SuggestConfig{DefaultLimit: 5, MinScore: 0.1}
	}
	return f(w.Suggest)
}
func withGraph(w *WikiConfig, f func(*GraphConfig) error) error {
	if w.Graph == nil {
		g := defaultGraphConfig()
		w.Graph = &g
	}
	return f(w.Graph)
}

func applyInt(dst *int, parse func() (int, error)) error {
	v, err := parse()
	if err != nil {
		return err
	}
	*dst = v
	return nil
}

func applyBool(dst *bool, value string) error {
	v, err := strconv.ParseBool(value)
	if err != nil {
		return err
	}
	*dst = v
	return nil
}

func splitComma(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

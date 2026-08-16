package wiki

import (
	"context"
	"encoding/gob"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	toml "github.com/pelletier/go-toml/v2"

	embedpkg "llm-wiki-go/internal/embed"
	"llm-wiki-go/internal/tokenizer"
)

// BM25 field weights: title and summary get boosted over body. This is the
// Go index's approximation of tantivy's per-field scoring (documented
// deviation: one weighted bag of terms instead of per-field BM25).
const (
	titleWeight   = 3
	summaryWeight = 2
	bodyWeight    = 1
)

// IndexDoc is one page in the search index.
type IndexDoc struct {
	Slug    string
	URI     string
	Title   string
	Summary string
	Type    string
	Status  string
	Tags    []string
	// Confidence is nil when the page declares none (neutral 1.0 — never
	// fabricated).
	Confidence  *float64
	Fields      map[string][]string // keyword fields (sources, concepts, ...)
	TextVals    map[string]string   // text fields (tldr, owner, ...)
	NumericVals map[string]float64
	BodyLinks   []string
	Body        string // raw body for excerpts and exports
	TF          map[string]int
	Len         int
	// Embedding is the unit-normalized semantic vector; nil when the
	// embedding pass did not run for this doc.
	Embedding []float32
}

// SearchIndex is an immutable in-memory BM25 index over the wiki pages.
type SearchIndex struct {
	Docs          []*IndexDoc
	bySlug        map[string]int
	df            map[string]int
	totalLen      int64
	TokenizerName string
}

// Doc returns the doc for a slug, or nil.
func (ix *SearchIndex) Doc(slug string) *IndexDoc {
	if i, ok := ix.bySlug[slug]; ok {
		return ix.Docs[i]
	}
	return nil
}

// DocsWithField returns slugs having a keyword field value equal to value.
func (ix *SearchIndex) DocsWithField(field, value string) []string {
	var out []string
	for _, d := range ix.Docs {
		for _, v := range d.Fields[field] {
			if v == value {
				out = append(out, d.Slug)
				break
			}
		}
	}
	return out
}

// Backlinks returns {slug, title} of pages whose body_links include slug,
// sorted by slug.
func (ix *SearchIndex) Backlinks(slug string) []map[string]string {
	type st struct{ slug, title string }
	var hits []st
	for _, d := range ix.Docs {
		for _, l := range d.BodyLinks {
			if l == slug {
				hits = append(hits, st{d.Slug, d.Title})
				break
			}
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].slug < hits[j].slug })
	out := make([]map[string]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, map[string]string{"slug": h.slug, "title": h.title})
	}
	return out
}

// IDF returns the inverse document frequency for a term (BM25 variant).
func (ix *SearchIndex) IDF(term string) float64 {
	n := float64(len(ix.Docs))
	if n == 0 {
		return 0
	}
	df := float64(ix.df[term])
	return log2(1 + (n-df+0.5)/(df+0.5))
}

func log2(x float64) float64 {
	if x <= 0 {
		return 0
	}
	return math.Log2(x)
}

// AvgLen returns the average weighted document length.
func (ix *SearchIndex) AvgLen() float64 {
	if len(ix.Docs) == 0 {
		return 0
	}
	return float64(ix.totalLen) / float64(len(ix.Docs))
}

// BuildIndexDoc converts a parsed page into an index doc, applying type
// aliases and field classification from the index schema. sourceDir is the
// containing directory used to normalize relative CommonMark links: the
// full slug for bundle pages, the parent for flat pages.
func BuildIndexDoc(slug Slug, wikiName string, page *ParsedPage, is *IndexSchema, tok *tokenizer.Tokenizer, sourceDir string) *IndexDoc {
	d := &IndexDoc{
		Slug:        slug.String(),
		URI:         fmt.Sprintf("wiki://%s/%s", wikiName, slug),
		Title:       page.Title(),
		Summary:     fmString(page.Frontmatter, "summary"),
		Type:        page.PageType(),
		Status:      page.Status(),
		Tags:        page.Tags(),
		Fields:      map[string][]string{},
		TextVals:    map[string]string{},
		NumericVals: map[string]float64{},
		Body:        page.Body,
	}
	if c, ok := Confidence(page.Frontmatter); ok {
		d.Confidence = &c
	}

	fm := page.Frontmatter
	// pass 1: non-aliased fields under their own name
	for key, val := range fm {
		if key == "title" || key == "summary" || key == "type" || key == "status" || key == "tags" || key == "confidence" {
			continue
		}
		if _, aliased := is.Aliases[key]; aliased {
			continue
		}
		if !is.HasField(key) {
			continue
		}
		applyFieldValue(d, is, key, val)
	}
	// pass 2: alias sources redirected to canonical when canonical is absent
	for alias, canonical := range is.Aliases {
		val, ok := fm[alias]
		if !ok {
			continue
		}
		if _, explicit := fm[canonical]; explicit {
			continue
		}
		if !is.HasField(canonical) {
			continue
		}
		applyFieldValue(d, is, canonical, val)
	}

	d.BodyLinks = ExtractBodyWikilinks(page.Body, []string{sourceDir})

	// weighted term bag
	tf := make(map[string]int, 64)
	addWeighted := func(tokens []string, w int) {
		for _, t := range tokens {
			tf[t] += w
		}
	}
	addWeighted(tok.Tokens(d.Title), titleWeight)
	addWeighted(tok.Tokens(d.Summary), summaryWeight)
	addWeighted(tok.Tokens(d.Body), bodyWeight)
	d.TF = tf
	length := 0
	for _, c := range tf {
		length += c
	}
	d.Len = length
	return d
}

func applyFieldValue(d *IndexDoc, is *IndexSchema, field string, val any) {
	switch is.Kind(field) {
	case FieldKeyword:
		d.Fields[field] = append(d.Fields[field], stringValues(val)...)
	case FieldNumeric:
		if f, ok := toFloat(val); ok {
			d.NumericVals[field] = f
		}
	default:
		d.TextVals[field] = joinValues(val)
	}
}

func stringValues(val any) []string {
	switch t := val.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, v := range t {
			if s, ok := v.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case bool:
		if t {
			return []string{"true"}
		}
		return []string{"false"}
	default:
		return nil
	}
}

func joinValues(val any) string {
	switch t := val.(type) {
	case string:
		return t
	case []any:
		var parts []string
		for _, v := range t {
			if s, ok := v.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func toFloat(val any) (float64, bool) {
	switch t := val.(type) {
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case float64:
		return t, true
	case float32:
		return float64(t), true
	}
	return 0, false
}

// NewSearchIndex builds an index over docs.
func NewSearchIndex(docs []*IndexDoc, tokenizerName string) *SearchIndex {
	ix := &SearchIndex{
		Docs:          docs,
		bySlug:        make(map[string]int, len(docs)),
		df:            map[string]int{},
		TokenizerName: tokenizerName,
	}
	ix.rebuildStats()
	return ix
}

// rebuildStats recomputes the derived lookup structures (gob round-trips
// only the exported fields).
func (ix *SearchIndex) rebuildStats() {
	if ix.bySlug == nil {
		ix.bySlug = make(map[string]int, len(ix.Docs))
	}
	if ix.df == nil {
		ix.df = map[string]int{}
	}
	ix.totalLen = 0
	for i, d := range ix.Docs {
		ix.bySlug[d.Slug] = i
		for term := range d.TF {
			ix.df[term]++
		}
		ix.totalLen += int64(d.Len)
	}
}

// IndexState is the persisted state.toml for one space's index.
type IndexState struct {
	SchemaHash string            `toml:"schema_hash"`
	Built      string            `toml:"built"` // RFC3339; empty = never
	Pages      int               `toml:"pages"`
	Sections   int               `toml:"sections"`
	Commit     string            `toml:"commit"`
	Types      map[string]string `toml:"types"`
	// EmbeddingModel pins the vector space: index and query must use the
	// same model; a change forces a full re-embed. Empty = no embeddings.
	EmbeddingModel string `toml:"embedding_model"`
	EmbeddingDims  int    `toml:"embedding_dims"`
}

// IndexReport describes a full rebuild.
type IndexReport struct {
	Wiki         string `json:"wiki"`
	PagesIndexed int    `json:"pages_indexed"`
	Skipped      int    `json:"skipped"`
	DurationMs   int64  `json:"duration_ms"`
}

// UpdateReport describes an incremental update.
type UpdateReport struct {
	Updated int `json:"updated"`
	Deleted int `json:"deleted"`
}

// IndexStatus is the health snapshot for wiki_index_status.
type IndexStatus struct {
	Built          *string `json:"built"`
	Pages          int     `json:"pages"`
	Sections       int     `json:"sections"`
	Stale          bool    `json:"stale"`
	Openable       bool    `json:"openable"`
	Queryable      bool    `json:"queryable"`
	EmbeddingModel string  `json:"embedding_model,omitempty"`
	EmbeddingDims  int     `json:"embedding_dims,omitempty"`
}

// StalenessKind classifies why an index is stale.
type StalenessKind int

// Staleness kinds.
const (
	StalenessCurrent StalenessKind = iota
	StalenessCommitChanged
	StalenessTypesChanged
	StalenessFullRebuildNeeded
)

// IndexManager owns the persisted index and its state for one space.
type IndexManager struct {
	WikiName  string
	IndexPath string

	mu           sync.RWMutex
	index        *SearchIndex
	state        IndexState
	changedTypes []string
	generation   atomic.Uint64
	tokenizer    *tokenizer.Tokenizer
	// embedClient, when non-nil, runs the embedding pass on rebuild/update.
	embedClient *embedpkg.Client
	// writeMu serializes writers (Rebuild/Update/RebuildTypes); m.mu stays
	// short-held so searches never block on embedding or git IO.
	writeMu sync.Mutex
}

// NewIndexManager creates a manager; the index is loaded lazily.
func NewIndexManager(wikiName, indexPath string, tok *tokenizer.Tokenizer) *IndexManager {
	return &IndexManager{WikiName: wikiName, IndexPath: indexPath, tokenizer: tok}
}

// SetEmbedClient attaches (or detaches with nil) the embedding client.
func (m *IndexManager) SetEmbedClient(c *embedpkg.Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.embedClient = c
}

// embedDocs fills doc embeddings in place via the given client and
// returns the vector dimensionality (0 when nothing was embedded).
// Must be called WITHOUT m.mu held — it performs network requests.
func (m *IndexManager) embedDocs(c *embedpkg.Client, docs []*IndexDoc) (dims int) {
	if c == nil || len(docs) == 0 {
		return 0
	}
	texts := make([]string, len(docs))
	for i, d := range docs {
		texts[i] = c.EmbedText(d.Title, d.Summary, d.Body)
	}
	vecs, err := c.Embed(context.Background(), texts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: embedding pass failed (index kept without vectors): %v\n", err)
		return 0
	}
	for i, d := range docs {
		if i < len(vecs) {
			d.Embedding = vecs[i]
		}
	}
	if len(vecs) > 0 {
		dims = len(vecs[0])
	}
	return dims
}

// Searcher returns the current in-memory index (nil if never built).
func (m *IndexManager) Searcher() *SearchIndex {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.index
}

// Generation returns the index generation (bumped on every change).
func (m *IndexManager) Generation() uint64 { return m.generation.Load() }

// LastCommit returns the commit the index was built at ("" = never).
func (m *IndexManager) LastCommit() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state.Commit
}

func (m *IndexManager) statePath() string { return filepath.Join(m.IndexPath, "state.toml") }
func (m *IndexManager) dataPath() string  { return filepath.Join(m.IndexPath, "index.gob") }

func (m *IndexManager) loadState() IndexState {
	raw, err := os.ReadFile(m.statePath())
	if err != nil {
		return IndexState{Types: map[string]string{}}
	}
	var st IndexState
	if err := toml.Unmarshal(raw, &st); err != nil {
		return IndexState{Types: map[string]string{}}
	}
	if st.Types == nil {
		st.Types = map[string]string{}
	}
	return st
}

func (m *IndexManager) saveState(st IndexState) error {
	if err := os.MkdirAll(m.IndexPath, 0o755); err != nil {
		return err
	}
	var buf strings.Builder
	if err := toml.NewEncoder(&buf).Encode(st); err != nil {
		return err
	}
	return os.WriteFile(m.statePath(), []byte(buf.String()), 0o644)
}

func (m *IndexManager) persist(index *SearchIndex, st IndexState) error {
	if err := os.MkdirAll(m.IndexPath, 0o755); err != nil {
		return err
	}
	if err := m.saveState(st); err != nil {
		return err
	}
	tmp := m.dataPath() + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := gob.NewEncoder(f).Encode(index); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, m.dataPath())
}

// loadIndex reads the persisted index into memory (nil when absent).
func (m *IndexManager) loadIndex() *SearchIndex {
	f, err := os.Open(m.dataPath())
	if err != nil {
		return nil
	}
	defer f.Close()
	var ix SearchIndex
	if err := gob.NewDecoder(f).Decode(&ix); err != nil {
		return nil
	}
	ix.rebuildStats()
	return &ix
}

// IndexFile indexes one markdown file into a doc.
func IndexFile(filePath, wikiRoot, wikiName string, is *IndexSchema, tok *tokenizer.Tokenizer) (*IndexDoc, error) {
	slug, err := SlugFromPath(filePath, wikiRoot)
	if err != nil {
		return nil, errSkip
	}
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return nil, errSkip
	}
	// source_dir: the full slug for bundle pages, the parent for flat pages
	sourceDir := path.Dir(slug.String())
	if path.Base(filepath.ToSlash(filePath)) == "index.md" {
		sourceDir = slug.String()
	}
	page := ParseFrontmatter(string(raw))
	return BuildIndexDoc(slug, wikiName, &page, is, tok, sourceDir), nil
}

var errSkip = fmt.Errorf("skip")

// Rebuild walks the wiki tree and rebuilds the full index.
func (m *IndexManager) Rebuild(wikiRoot, repoRoot string, is *IndexSchema, registry *TypeRegistry) (IndexReport, error) {
	start := time.Now()
	var docs []*IndexDoc
	skipped := 0
	err := filepath.WalkDir(wikiRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		doc, err := IndexFile(path, wikiRoot, m.WikiName, is, m.tokenizer)
		if err != nil {
			skipped++
			return nil
		}
		docs = append(docs, doc)
		return nil
	})
	if err != nil {
		return IndexReport{}, err
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Slug < docs[j].Slug })
	m.mu.RLock()
	embedder := m.embedClient
	m.mu.RUnlock()
	dims := m.embedDocs(embedder, docs)
	index := NewSearchIndex(docs, m.tokenizer.Name())

	pages, sections := countPagesSections(docs)
	st := IndexState{
		Built:      time.Now().Format(time.RFC3339),
		Pages:      pages,
		Sections:   sections,
		Commit:     GitCurrentHead(repoRoot),
		Types:      registry.PerTypeHashes,
		SchemaHash: registry.GlobalHash,
	}
	st = applyEmbedState(st, embedder, dims)
	if err := m.persist(index, st); err != nil {
		return IndexReport{}, err
	}
	m.mu.Lock()
	m.index = index
	m.state = st
	m.mu.Unlock()
	m.generation.Add(1)
	return IndexReport{Wiki: m.WikiName, PagesIndexed: len(docs), Skipped: skipped, DurationMs: time.Since(start).Milliseconds()}, nil
}

// Update applies git-detected changes since lastCommit. Writers are
// serialized by writeMu; m.mu is only held for snapshot/install so
// concurrent searches never wait on embedding or git IO.
func (m *IndexManager) Update(wikiRoot, repoRoot string, lastCommit string, is *IndexSchema, registry *TypeRegistry) (UpdateReport, error) {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()

	changes := CollectChangedFiles(repoRoot, wikiRoot, lastCommit)
	if len(changes) == 0 {
		return UpdateReport{}, nil
	}

	// 1. short lock: snapshot docs/state, detach the embed client
	m.mu.Lock()
	var docs []*IndexDoc
	if m.index != nil {
		docs = append(docs, m.index.Docs...)
	} else if loaded := m.loadIndex(); loaded != nil {
		docs = loaded.Docs
		m.index = loaded
	}
	prev := m.state
	if prev.Types == nil {
		prev = m.loadState()
	}
	embedder := m.embedClient
	m.mu.Unlock()

	// 2. lock-free: apply changes to the private copy. Fresh IndexDoc
	// objects are invisible to readers until install; retained docs keep
	// their vectors, so only fresh ones get (re)embedded.
	bySlug := map[string]int{}
	for i, d := range docs {
		bySlug[d.Slug] = i
	}
	report := UpdateReport{}
	wikiPrefix := relUnder(repoRoot, wikiRoot)
	for path, status := range changes {
		full := filepath.Join(wikiRoot, strings.TrimPrefix(path, wikiPrefix+"/"))
		slugStr := strings.TrimPrefix(strings.TrimSuffix(path, ".md"), wikiPrefix+"/")
		slugStr = strings.TrimSuffix(slugStr, "/index")
		if status == DeltaDeleted {
			if i, ok := bySlug[slugStr]; ok {
				docs = append(docs[:i], docs[i+1:]...)
				reindexBySlug(bySlug, docs)
				report.Deleted++
			}
			continue
		}
		doc, err := IndexFile(full, wikiRoot, m.WikiName, is, m.tokenizer)
		if err != nil || doc == nil {
			continue
		}
		if i, ok := bySlug[doc.Slug]; ok {
			docs[i] = doc
		} else {
			docs = append(docs, doc)
			bySlug[doc.Slug] = len(docs) - 1
		}
		report.Updated++
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Slug < docs[j].Slug })
	var dims int
	if embedder != nil {
		var fresh []*IndexDoc
		for _, d := range docs {
			if d.Embedding == nil {
				fresh = append(fresh, d)
			}
		}
		dims = m.embedDocs(embedder, fresh)
	}

	// 3. short lock: install
	index := NewSearchIndex(docs, m.tokenizer.Name())
	pages, sections := countPagesSections(docs)
	st := prev
	st.Built = time.Now().Format(time.RFC3339)
	st.Pages = pages
	st.Sections = sections
	st.Commit = GitCurrentHead(repoRoot)
	st.Types = registry.PerTypeHashes
	st.SchemaHash = registry.GlobalHash
	st = applyEmbedState(st, embedder, dims)

	m.mu.Lock()
	if err := m.persist(index, st); err != nil {
		m.mu.Unlock()
		return report, err
	}
	m.index = index
	m.state = st
	m.mu.Unlock()
	m.generation.Add(1)
	return report, nil
}

func countPagesSections(docs []*IndexDoc) (pages, sections int) {
	for _, d := range docs {
		if d.Type == "section" {
			sections++
		} else {
			pages++
		}
	}
	return pages, sections
}

func reindexBySlug(bySlug map[string]int, docs []*IndexDoc) {
	for k := range bySlug {
		delete(bySlug, k)
	}
	for i, d := range docs {
		bySlug[d.Slug] = i
	}
}

// applyEmbedState records the embedding anchor on the index state.
func applyEmbedState(st IndexState, embedder *embedpkg.Client, dims int) IndexState {
	if embedder != nil {
		st.EmbeddingModel = embedder.Model()
		if dims > 0 {
			st.EmbeddingDims = dims
		}
	} else {
		st.EmbeddingModel, st.EmbeddingDims = "", 0
	}
	return st
}

// RebuildTypes reindexes only the docs of the given types. Same locking
// discipline as Update: writers serialized, m.mu held only for
// snapshot/install, embedding runs lock-free.
func (m *IndexManager) RebuildTypes(types []string, wikiRoot, repoRoot string, is *IndexSchema, registry *TypeRegistry) error {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()

	typeSet := map[string]bool{}
	for _, t := range types {
		typeSet[t] = true
	}

	m.mu.Lock()
	var docs []*IndexDoc
	if m.index != nil {
		for _, d := range m.index.Docs {
			if !typeSet[d.Type] {
				docs = append(docs, d)
			}
		}
	}
	prev := m.state
	embedder := m.embedClient
	m.mu.Unlock()

	err := filepath.WalkDir(wikiRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		doc, err := IndexFile(path, wikiRoot, m.WikiName, is, m.tokenizer)
		if err != nil || doc == nil || !typeSet[doc.Type] {
			return nil
		}
		docs = append(docs, doc)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Slug < docs[j].Slug })
	var dims int
	if embedder != nil {
		var fresh []*IndexDoc
		for _, d := range docs {
			if d.Embedding == nil {
				fresh = append(fresh, d)
			}
		}
		dims = m.embedDocs(embedder, fresh)
	}
	index := NewSearchIndex(docs, m.tokenizer.Name())

	pages, sections := countPagesSections(docs)
	st := prev
	st.Built = time.Now().Format(time.RFC3339)
	st.Pages = pages
	st.Sections = sections
	st.Commit = GitCurrentHead(repoRoot)
	st.Types = registry.PerTypeHashes
	st.SchemaHash = registry.GlobalHash
	st = applyEmbedState(st, embedder, dims)

	m.mu.Lock()
	if err := m.persist(index, st); err != nil {
		m.mu.Unlock()
		return err
	}
	m.index = index
	m.state = st
	m.mu.Unlock()
	m.generation.Add(1)
	return nil
}

// Status reports index health for the current repo state.
func (m *IndexManager) Status(repoRoot string) *IndexStatus {
	st := m.loadState()
	diskGlobal, _, err := ComputeDiskHashes(repoRoot)
	head := GitCurrentHead(repoRoot)
	expectedModel := ""
	if m.embedClient != nil {
		expectedModel = m.embedClient.Model()
	}
	status := &IndexStatus{
		Pages:    st.Pages,
		Sections: st.Sections,
		Stale: st.Built == "" || st.Commit != head || err != nil || st.SchemaHash != diskGlobal ||
			embedModelMismatch(st, expectedModel),
		Openable:       fileExists(m.dataPath()),
		Queryable:      false,
		EmbeddingModel: st.EmbeddingModel,
		EmbeddingDims:  st.EmbeddingDims,
	}
	if st.Built != "" {
		b := st.Built
		status.Built = &b
	}
	if status.Openable {
		if ix := m.loadIndex(); ix != nil && len(ix.Docs) >= 0 {
			status.Queryable = true
		}
	}
	return status
}

// embedModelMismatch reports a vector-space change: state was built with
// a different embedding model than currently configured (including
// enabled↔disabled transitions with existing vectors).
func embedModelMismatch(st IndexState, expectedModel string) bool {
	if expectedModel != "" {
		return st.EmbeddingModel != expectedModel
	}
	return false // embedding disabled: stale vectors are harmless, not stale state
}

// Staleness classifies the staleness for smart rebuilds. embedModel is
// the currently configured embedding model ("" = disabled); a mismatch
// with state forces a full rebuild.
func (m *IndexManager) Staleness(repoRoot, embedModel string) StalenessKind {
	st := m.loadState()
	if st.Built == "" {
		return StalenessFullRebuildNeeded
	}
	if embedModelMismatch(st, embedModel) {
		return StalenessFullRebuildNeeded
	}
	diskGlobal, diskPerType, err := ComputeDiskHashes(repoRoot)
	if err != nil {
		return StalenessFullRebuildNeeded
	}
	head := GitCurrentHead(repoRoot)
	commitChanged := st.Commit != head
	schemaChanged := st.SchemaHash != diskGlobal
	if !commitChanged && !schemaChanged {
		return StalenessCurrent
	}
	if commitChanged && !schemaChanged {
		return StalenessCommitChanged
	}
	// schema differs: check per-type hashes for a partial rebuild
	if maps.Equal(st.Types, diskPerType) && !schemaChanged {
		return StalenessFullRebuildNeeded
	}
	var changed []string
	for name, h := range diskPerType {
		if st.Types[name] != h {
			changed = append(changed, name)
		}
	}
	for name := range st.Types {
		if _, ok := diskPerType[name]; !ok {
			changed = append(changed, name)
		}
	}
	if len(changed) == 0 {
		return StalenessFullRebuildNeeded
	}
	// expose changed types through the manager for the caller
	m.mu.Lock()
	m.changedTypes = changed
	m.mu.Unlock()
	return StalenessTypesChanged
}

// ChangedTypes returns the type names detected by the last Staleness call.
func (m *IndexManager) ChangedTypes() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.changedTypes
}

// Open loads the persisted index and state for serving. A tokenizer
// change invalidates the persisted index (forces a rebuild).
func (m *IndexManager) Open() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Built == "" {
		m.state = m.loadState()
	}
	if m.index == nil {
		m.index = m.loadIndex()
		if m.index != nil && m.index.TokenizerName != "" && m.index.TokenizerName != m.tokenizer.Name() {
			m.index = nil // tokenizer changed → force rebuild
		}
	}
}

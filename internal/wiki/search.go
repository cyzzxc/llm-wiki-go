package wiki

import (
	"fmt"
	"html"
	"sort"
	"strings"

	"llm-wiki-go/internal/tokenizer"
)

// BM25 parameters (tantivy defaults).
const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// PageRef is a single search result with BM25 score and excerpt.
type PageRef struct {
	Slug       string  `json:"slug"`
	URI        string  `json:"uri"`
	Title      string  `json:"title"`
	Score      float64 `json:"score"`
	Confidence float64 `json:"confidence"`
	Excerpt    *string `json:"excerpt,omitempty"`
	Summary    *string `json:"summary,omitempty"`
}

// PageSummary is lightweight page metadata for listings.
type PageSummary struct {
	Slug       string   `json:"slug"`
	URI        string   `json:"uri"`
	Title      string   `json:"title"`
	Type       string   `json:"type"`
	Status     string   `json:"status"`
	Tags       []string `json:"tags"`
	Confidence float64  `json:"confidence"`
	Summary    *string  `json:"summary,omitempty"`
}

// FacetCounts is the distribution of type/status/tags values.
type FacetCounts struct {
	Type   map[string]uint64 `json:"type,omitempty"`
	Status map[string]uint64 `json:"status,omitempty"`
	Tags   map[string]uint64 `json:"tags,omitempty"`
}

func (f FacetCounts) empty() bool {
	return len(f.Type) == 0 && len(f.Status) == 0 && len(f.Tags) == 0
}

// SearchResult is the full outcome of a search.
type SearchResult struct {
	Results []PageRef   `json:"results"`
	Facets  FacetCounts `json:"facets"`
}

// PageList is a paginated page listing.
type PageList struct {
	Pages    []PageSummary `json:"pages"`
	Total    int           `json:"total"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
	Facets   FacetCounts   `json:"facets,omitempty"`
}

// SearchOptions parameterize a BM25 search.
type SearchOptions struct {
	NoExcerpt       bool
	IncludeSections bool
	TopK            int
	Type            string
	FacetsTopTags   int
	SearchConfig    SearchConfig
}

// DefaultSearchOptions mirrors the Rust defaults.
func DefaultSearchOptions() SearchOptions {
	return SearchOptions{
		TopK:          10,
		FacetsTopTags: 10,
		SearchConfig:  defaultSearchConfig(),
	}
}

// ListOptions parameterize a paginated list.
type ListOptions struct {
	Type          string
	Status        string
	Page          int
	PageSize      int
	FacetsTopTags int
}

// Search runs a BM25 query against one index.
func Search(queryStr string, opts SearchOptions, ix *SearchIndex, tok *tokenizer.Tokenizer) (*SearchResult, error) {
	matches := matchingDocs(ix, opts.IncludeSections, opts.Type)

	terms := tok.Tokens(queryStr)
	type scored struct {
		doc   *IndexDoc
		score float64
	}
	var hits []scored
	avg := ix.AvgLen()
	n := float64(len(ix.Docs))
	for _, d := range matches {
		score := 0.0
		for _, term := range terms {
			tf := float64(d.TF[term])
			if tf == 0 {
				continue
			}
			df := float64(ix.df[term])
			idf := log2(1 + (n-df+0.5)/(df+0.5))
			denom := tf + bm25K1*(1-bm25B+bm25B*float64(d.Len)/avg)
			score += idf * tf * (bm25K1 + 1) / denom
		}
		if score <= 0 {
			continue
		}
		hits = append(hits, scored{d, score * statusMultiplier(opts.SearchConfig, d.Status) * confidenceMultiplier(d)})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].doc.Slug < hits[j].doc.Slug
	})
	if len(hits) > opts.TopK {
		hits = hits[:opts.TopK]
	}

	results := make([]PageRef, 0, len(hits))
	for _, h := range hits {
		pr := PageRef{
			Slug:       h.doc.Slug,
			URI:        h.doc.URI,
			Title:      h.doc.Title,
			Score:      round2(h.score),
			Confidence: confidenceMultiplier(h.doc),
		}
		if !opts.NoExcerpt {
			e := makeExcerpt(h.doc.Body, terms)
			pr.Excerpt = &e
		}
		if h.doc.Summary != "" {
			s := h.doc.Summary
			pr.Summary = &s
		}
		results = append(results, pr)
	}

	// facets: type unfiltered (by query but without type filter), status
	// and tags filtered — matching the Rust semantics of "type is
	// unfiltered, status and tags are filtered" (filters, not the query)
	unfiltered := matchingDocs(ix, opts.IncludeSections, "")
	return &SearchResult{
		Results: results,
		Facets: FacetCounts{
			Type:   collectFacet(unfiltered, "type", 0),
			Status: collectFacet(matches, "status", 0),
			Tags:   collectFacet(matches, "tags", opts.FacetsTopTags),
		},
	}, nil
}

func matchingDocs(ix *SearchIndex, includeSections bool, typeFilter string) []*IndexDoc {
	var out []*IndexDoc
	for _, d := range ix.Docs {
		if !includeSections && d.Type == "section" {
			continue
		}
		if typeFilter != "" && d.Type != typeFilter {
			continue
		}
		out = append(out, d)
	}
	return out
}

func statusMultiplier(sc SearchConfig, status string) float64 {
	if m, ok := sc.Status[status]; ok && status != "" {
		return m
	}
	if m, ok := sc.Status["unknown"]; ok {
		return m
	}
	return 0.9
}

func confidenceMultiplier(d *IndexDoc) float64 {
	if d.Confidence == nil {
		return 1.0
	}
	return *d.Confidence
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }

// List returns a slug-sorted paginated listing with facets.
func List(opts ListOptions, ix *SearchIndex) (*PageList, error) {
	if opts.PageSize == 0 {
		return nil, fmt.Errorf("page_size must be at least 1")
	}
	if opts.Page < 1 {
		opts.Page = 1
	}
	var matches []*IndexDoc
	for _, d := range ix.Docs {
		if opts.Type != "" && d.Type != opts.Type {
			continue
		}
		if opts.Status != "" && d.Status != opts.Status {
			continue
		}
		matches = append(matches, d)
	}
	total := len(matches)
	offset := (opts.Page - 1) * opts.PageSize
	end := offset + opts.PageSize
	if offset > total {
		offset = total
	}
	if end > total {
		end = total
	}
	window := matches[offset:end]

	pages := make([]PageSummary, 0, len(window))
	for _, d := range window {
		ps := PageSummary{
			Slug:       d.Slug,
			URI:        d.URI,
			Title:      d.Title,
			Type:       d.Type,
			Status:     d.Status,
			Tags:       d.Tags,
			Confidence: confidenceMultiplier(d),
		}
		if d.Summary != "" {
			s := d.Summary
			ps.Summary = &s
		}
		pages = append(pages, ps)
	}
	return &PageList{
		Pages:    pages,
		Total:    total,
		Page:     opts.Page,
		PageSize: opts.PageSize,
		Facets: FacetCounts{
			Type:   collectFacet(ix.Docs, "type", 0),
			Status: collectFacet(matches, "status", 0),
			Tags:   collectFacet(matches, "tags", opts.FacetsTopTags),
		},
	}, nil
}

// NamedIndex pairs a wiki name with its index for cross-wiki search.
type NamedIndex struct {
	Name  string
	Index *SearchIndex
}

// SearchAll merges results across wikis by score.
func SearchAll(queryStr string, opts SearchOptions, wikis []NamedIndex, tok *tokenizer.Tokenizer) (*SearchResult, error) {
	merged := FacetCounts{Type: map[string]uint64{}, Status: map[string]uint64{}, Tags: map[string]uint64{}}
	var all []PageRef
	for _, w := range wikis {
		sr, err := Search(queryStr, opts, w.Index, tok)
		if err != nil {
			continue
		}
		all = append(all, sr.Results...)
		for k, v := range sr.Facets.Type {
			merged.Type[k] += v
		}
		for k, v := range sr.Facets.Status {
			merged.Status[k] += v
		}
		for k, v := range sr.Facets.Tags {
			merged.Tags[k] += v
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Score != all[j].Score {
			return all[i].Score > all[j].Score
		}
		return all[i].Slug < all[j].Slug
	})
	if len(all) > opts.TopK {
		all = all[:opts.TopK]
	}
	if opts.FacetsTopTags > 0 && len(merged.Tags) > opts.FacetsTopTags {
		merged.Tags = topNMap(merged.Tags, opts.FacetsTopTags)
	}
	return &SearchResult{Results: all, Facets: merged}, nil
}

func collectFacet(docs []*IndexDoc, field string, topN int) map[string]uint64 {
	counts := map[string]uint64{}
	for _, d := range docs {
		switch field {
		case "type":
			if d.Type != "" {
				counts[d.Type]++
			}
		case "status":
			if d.Status != "" {
				counts[d.Status]++
			}
		case "tags":
			for _, t := range d.Tags {
				if t != "" {
					counts[t]++
				}
			}
		}
	}
	if topN > 0 && len(counts) > topN {
		return topNMap(counts, topN)
	}
	return counts
}

func topNMap(m map[string]uint64, n int) map[string]uint64 {
	type kv struct {
		k string
		v uint64
	}
	entries := make([]kv, 0, len(m))
	for k, v := range m {
		entries = append(entries, kv{k, v})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].v != entries[j].v {
			return entries[i].v > entries[j].v
		}
		return entries[i].k < entries[j].k
	})
	out := make(map[string]uint64, n)
	for _, e := range entries[:n] {
		out[e.k] = e.v
	}
	return out
}

// makeExcerpt builds an HTML-highlighted window around the first query
// term occurrence (tantivy-style <b> wrapping, HTML-escaped text).
func makeExcerpt(body string, terms []string) string {
	if body == "" || len(terms) == 0 {
		if body == "" {
			return ""
		}
		return escapeAndWindow(body, 0, excerptRadius*2)
	}
	lower := strings.ToLower(body)
	bestPos := -1
	for _, t := range terms {
		if t == "" {
			continue
		}
		if idx := strings.Index(lower, strings.ToLower(t)); idx >= 0 && (bestPos == -1 || idx < bestPos) {
			bestPos = idx
		}
	}
	if bestPos < 0 {
		return escapeAndWindow(body, 0, excerptRadius*2)
	}
	start := bestPos - excerptRadius
	if start < 0 {
		start = 0
	}
	// rune-align the window
	for start > 0 && !runeStartAt(body, start) {
		start--
	}
	out := escapeAndWindow(body, start, excerptRadius*2)
	// highlight terms (post-escape, term text is unchanged by escaping
	// unless it contains &<> — strip such terms)
	for _, t := range terms {
		if t == "" || strings.ContainsAny(t, "&<>\"") {
			continue
		}
		out = highlightCaseInsensitive(out, t)
	}
	if start > 0 {
		out = "…" + out
	}
	if start+excerptRadius*2 < len(body) {
		out += "…"
	}
	return out
}

const excerptRadius = 80

func escapeAndWindow(s string, start, n int) string {
	if start >= len(s) {
		return ""
	}
	end := start + n
	if end > len(s) {
		end = len(s)
	}
	for end > start && end < len(s) && !runeStartAt(s, end) {
		end--
	}
	return html.EscapeString(s[start:end])
}

func highlightCaseInsensitive(s, term string) string {
	lower := strings.ToLower(s)
	lt := strings.ToLower(term)
	var b strings.Builder
	last := 0
	for {
		idx := strings.Index(lower[last:], lt)
		if idx < 0 {
			break
		}
		pos := last + idx
		b.WriteString(s[last:pos])
		b.WriteString("<b>")
		b.WriteString(s[pos : pos+len(term)])
		b.WriteString("</b>")
		last = pos + len(term)
	}
	b.WriteString(s[last:])
	return b.String()
}

// RenderListLLMS renders a PageList as LLM-optimized markdown.
func RenderListLLMS(result *PageList) string {
	byType := map[string][]*PageSummary{}
	for i := range result.Pages {
		p := &result.Pages[i]
		byType[p.Type] = append(byType[p.Type], p)
	}
	type group struct {
		name  string
		pages []*PageSummary
	}
	var groups []group
	for name, pages := range byType {
		groups = append(groups, group{name, pages})
	}
	sort.Slice(groups, func(i, j int) bool {
		if len(groups[i].pages) != len(groups[j].pages) {
			return len(groups[i].pages) > len(groups[j].pages)
		}
		return groups[i].name < groups[j].name
	})

	var out strings.Builder
	for _, g := range groups {
		sort.Slice(g.pages, func(i, j int) bool {
			if g.pages[i].Confidence != g.pages[j].Confidence {
				return g.pages[i].Confidence > g.pages[j].Confidence
			}
			return g.pages[i].Title < g.pages[j].Title
		})
		fmt.Fprintf(&out, "## %s (%d)\n\n", g.name, len(g.pages))
		for _, p := range g.pages {
			summary := ""
			if p.Summary != nil {
				summary = *p.Summary
			}
			if p.Status == "archived" {
				if summary == "" {
					fmt.Fprintf(&out, "- ~~[%s](%s)~~\n", p.Title, p.URI)
				} else {
					fmt.Fprintf(&out, "- ~~[%s](%s): %s~~\n", p.Title, p.URI, summary)
				}
			} else if summary == "" {
				fmt.Fprintf(&out, "- [%s](%s)\n", p.Title, p.URI)
			} else {
				fmt.Fprintf(&out, "- [%s](%s): %s\n", p.Title, p.URI, summary)
			}
		}
		out.WriteString("\n")
	}
	if result.Total > result.PageSize {
		totalPages := (result.Total + result.PageSize - 1) / max(result.PageSize, 1)
		fmt.Fprintf(&out, "_Page %d/%d — %d total pages_\n", result.Page, totalPages, result.Total)
	}
	return out.String()
}

// RenderSearchLLMS renders a SearchResult as LLM-optimized markdown.
func RenderSearchLLMS(result *SearchResult) string {
	if len(result.Results) == 0 {
		return "No results found.\n"
	}
	var out strings.Builder
	for _, r := range result.Results {
		summary := ""
		if r.Summary != nil {
			summary = *r.Summary
		}
		if summary == "" {
			fmt.Fprintf(&out, "- [%s](%s)\n", r.Title, r.URI)
		} else {
			fmt.Fprintf(&out, "- [%s](%s): %s\n", r.Title, r.URI, summary)
		}
	}
	return out.String()
}

// runeStartAt reports whether the byte at i begins a UTF-8 rune.
func runeStartAt(s string, i int) bool { return s[i]&0xC0 != 0x80 }

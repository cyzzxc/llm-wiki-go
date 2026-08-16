package wiki

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// SearchParams parameterize the search ops entry point. Mode selects
// keyword (default) / semantic / hybrid ranking.
type SearchParams struct {
	Query           string
	TypeFilter      string
	NoExcerpt       bool
	TopK            int
	IncludeSections bool
	CrossWiki       bool
	Mode            string
}

// OpsSearch runs a search against one wiki or all mounted wikis.
func OpsSearch(engine *WikiEngine, wikiName string, p SearchParams) (*SearchResult, error) {
	space, err := engine.Space(wikiName)
	if err != nil {
		return nil, err
	}
	mode := p.Mode
	if mode == "" {
		mode = ModeKeyword
	}
	var queryEmb []float32
	hybridWeight := 0.5
	if mode != ModeKeyword {
		if space.Embed == nil {
			return nil, fmt.Errorf("semantic search not configured — set [embedding] in config and rebuild the index")
		}
		vecs, err := space.Embed.Embed(context.Background(), []string{p.Query})
		if err != nil {
			return nil, fmt.Errorf("query embedding failed: %w", err)
		}
		if len(vecs) > 0 {
			queryEmb = vecs[0]
		}
		hybridWeight = space.Embed.Config().HybridWeight
	}
	opts := SearchOptions{
		NoExcerpt:       p.NoExcerpt,
		IncludeSections: p.IncludeSections,
		TopK:            orDefault(p.TopK, space.Resolved.Defaults.SearchTopK),
		Type:            p.TypeFilter,
		FacetsTopTags:   space.Resolved.Defaults.FacetsTopTags,
		SearchConfig:    space.Resolved.Search,
		Mode:            mode,
		QueryEmbedding:  queryEmb,
		HybridWeight:    hybridWeight,
	}
	ix := space.IndexManager.Searcher()
	if ix == nil {
		return &SearchResult{Results: []PageRef{}, Facets: FacetCounts{}}, nil
	}
	if p.CrossWiki {
		var wikis []NamedIndex
		for _, s := range engine.SpacesList() {
			if i := s.IndexManager.Searcher(); i != nil {
				wikis = append(wikis, NamedIndex{s.Name, i})
			}
		}
		return SearchAll(p.Query, opts, wikis, space.Tokenizer)
	}
	return Search(p.Query, opts, ix, space.Tokenizer)
}

// OpsList returns a paginated listing for one wiki.
func OpsList(engine *WikiEngine, wikiName, typeFilter, statusFilter string, page, pageSize int) (*PageList, *SpaceContext, error) {
	space, err := engine.Space(wikiName)
	if err != nil {
		return nil, nil, err
	}
	opts := ListOptions{
		Type:          typeFilter,
		Status:        statusFilter,
		Page:          orDefault(page, 1),
		PageSize:      orDefault(pageSize, space.Resolved.Defaults.ListPageSize),
		FacetsTopTags: space.Resolved.Defaults.FacetsTopTags,
	}
	ix := space.IndexManager.Searcher()
	if ix == nil {
		return &PageList{Pages: []PageSummary{}, Page: opts.Page, PageSize: opts.PageSize}, space, nil
	}
	list, err := List(opts, ix)
	return list, space, err
}

// BacklinksQuery returns pages whose body_links reference the slug.
func BacklinksQuery(engine *WikiEngine, wikiName, slug string) []map[string]string {
	space, err := engine.Space(wikiName)
	if err != nil {
		return nil
	}
	ix := space.IndexManager.Searcher()
	if ix == nil {
		return nil
	}
	return ix.Backlinks(slug)
}

// RecentPage is a page plus its last_updated date, ordered by recency for
// web surfaces (home "recently tended", RSS feed).
type RecentPage struct {
	Slug        string
	Title       string
	Type        string
	Summary     string
	LastUpdated string
}

// OpsRecentPages returns up to limit pages sorted by last_updated
// descending (ISO dates sort lexicographically; undated pages last,
// slug-ordered as tiebreak).
func OpsRecentPages(engine *WikiEngine, wikiName string, limit int) ([]RecentPage, error) {
	space, err := engine.Space(wikiName)
	if err != nil {
		return nil, err
	}
	ix := space.IndexManager.Searcher()
	if ix == nil {
		return []RecentPage{}, nil
	}
	var pages []RecentPage
	for _, d := range ix.Docs {
		pages = append(pages, RecentPage{
			Slug: d.Slug, Title: d.Title, Type: d.Type,
			Summary: d.Summary, LastUpdated: d.TextVals["last_updated"],
		})
	}
	sort.Slice(pages, func(i, j int) bool {
		a, b := pages[i], pages[j]
		if a.LastUpdated != b.LastUpdated {
			if a.LastUpdated == "" {
				return false
			}
			if b.LastUpdated == "" {
				return true
			}
			return a.LastUpdated > b.LastUpdated
		}
		return a.Slug < b.Slug
	})
	if len(pages) > limit {
		pages = pages[:limit]
	}
	return pages, nil
}

// Suggestion is one related-page suggestion.
type Suggestion struct {
	Slug   string  `json:"slug"`
	URI    string  `json:"uri"`
	Title  string  `json:"title"`
	Type   string  `json:"type"`
	Score  float64 `json:"score"`
	Reason string  `json:"reason"`
	Field  string  `json:"field"`
}

// sourceTypes mirror the Rust SOURCE_TYPES set for suggest_field.
var suggestSourceTypes = map[string]bool{
	"paper": true, "article": true, "documentation": true, "clipping": true,
	"transcript": true, "note": true, "data": true, "book-chapter": true, "thread": true,
}

// OpsSuggest suggests related pages via 4 strategies: tag overlap, graph
// 2-hop, BM25 similarity, and community peers.
func OpsSuggest(engine *WikiEngine, wikiName, uri string, limit int) ([]Suggestion, error) {
	space, err := engine.Space(wikiName)
	if err != nil {
		return nil, err
	}
	slug := uri
	if parsed, err := ParseWikiUri(uri); err == nil {
		slug = parsed.Slug.String()
	}
	ix := space.IndexManager.Searcher()
	if ix == nil {
		return []Suggestion{}, nil
	}
	doc := ix.Doc(slug)
	if doc == nil {
		return nil, fmt.Errorf("page not found: %s", slug)
	}

	if limit == 0 {
		limit = space.Resolved.Suggest.DefaultLimit
	}
	minScore := space.Resolved.Suggest.MinScore

	linked := map[string]bool{slug: true}
	for _, l := range doc.Fields["sources"] {
		linked[l] = true
	}
	for _, l := range doc.Fields["concepts"] {
		linked[l] = true
	}
	for _, l := range doc.BodyLinks {
		linked[l] = true
	}
	for _, l := range doc.Fields["document_refs"] {
		linked[l] = true
	}

	type cand struct {
		score  float64
		reason string
	}
	candidates := map[string]*cand{}
	addCand := func(slugStr string, score float64, reason string) {
		if linked[slugStr] {
			return
		}
		if c, ok := candidates[slugStr]; !ok || score > c.score {
			candidates[slugStr] = &cand{score, reason}
		}
	}

	// strategy 1: tag overlap via per-tag BM25
	for _, tag := range doc.Tags {
		res, err := Search(tag, SearchOptions{TopK: 20, SearchConfig: space.Resolved.Search}, ix, space.Tokenizer)
		if err != nil {
			continue
		}
		for _, r := range res.Results {
			d := ix.Doc(r.Slug)
			if d == nil {
				continue
			}
			shared := 0
			for _, t := range doc.Tags {
				for _, dt := range d.Tags {
					if t == dt {
						shared++
						break
					}
				}
			}
			if shared > 0 {
				denom := len(d.Tags)
				if denom < 1 {
					denom = 1
				}
				addCand(r.Slug, float64(shared)/float64(denom), fmt.Sprintf("shares tags: %s", strings.Join(doc.Tags, ", ")))
			}
		}
	}

	// strategy 2: graph 2-hop neighbors
	if g, err := space.GetOrBuildGraph(GraphFilter{}); err == nil {
		oneHop := map[string]bool{}
		var rootIdx = -1
		for i, n := range g.Nodes {
			if n.Slug == slug {
				rootIdx = i
				break
			}
		}
		if rootIdx >= 0 {
			for _, nb := range append(g.OutNeighbors(rootIdx), g.InNeighbors(rootIdx)...) {
				nbSlug := g.Nodes[nb].Slug
				oneHop[nbSlug] = true
				for _, nb2 := range append(g.OutNeighbors(nb), g.InNeighbors(nb)...) {
					nb2Slug := g.Nodes[nb2].Slug
					if !oneHop[nb2Slug] && nb2Slug != slug {
						addCand(nb2Slug, 0.5, fmt.Sprintf("2 hops via %s", nbSlug))
					}
				}
			}
		}
	}

	// strategy 3: BM25 similarity on title + summary
	if q := strings.TrimSpace(doc.Title + " " + doc.Summary); q != "" {
		res, err := Search(q, SearchOptions{TopK: 10, NoExcerpt: true, SearchConfig: space.Resolved.Search}, ix, space.Tokenizer)
		if err == nil && len(res.Results) > 0 {
			maxScore := res.Results[0].Score
			if maxScore < 0.001 {
				maxScore = 0.001
			}
			for _, r := range res.Results {
				if r.Slug == slug {
					continue
				}
				addCand(r.Slug, r.Score/maxScore*0.7, "similar content")
			}
		}
	}

	// strategy 4: community peers
	if _, communityMap := space.CommunityData(space.Resolved.Graph.MinNodesForCommunities); communityMap != nil {
		if c, ok := communityMap[slug]; ok {
			var peers []string
			for peerSlug, pc := range communityMap {
				if pc == c && peerSlug != slug && candidates[peerSlug] == nil && !linked[peerSlug] {
					peers = append(peers, peerSlug)
				}
			}
			sort.Strings(peers)
			limitPeers := space.Resolved.Graph.CommunitySuggestionsLimit
			for i, peer := range peers {
				if i >= limitPeers {
					break
				}
				addCand(peer, 0.4, "same knowledge cluster")
			}
		}
	}

	var out []Suggestion
	for candSlug, c := range candidates {
		if c.score < minScore {
			continue
		}
		d := ix.Doc(candSlug)
		if d == nil {
			continue
		}
		out = append(out, Suggestion{
			Slug:   candSlug,
			URI:    fmt.Sprintf("wiki://%s/%s", space.Name, candSlug),
			Title:  d.Title,
			Type:   d.Type,
			Score:  round2(c.score),
			Reason: c.reason,
			Field:  suggestField(space.TypeRegistry, doc.Type, d.Type),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Slug < out[j].Slug
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// suggestField picks the frontmatter edge field to record the link in.
func suggestField(registry *TypeRegistry, sourceType, candidateType string) string {
	for _, decl := range registry.Edges(sourceType) {
		for _, tt := range decl.TargetTypes {
			if tt == candidateType {
				return decl.Field
			}
		}
		if suggestSourceTypes[sourceType] && suggestSourceTypes[candidateType] {
			return decl.Field
		}
	}
	return "[[wikilink]]"
}

func orDefault(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

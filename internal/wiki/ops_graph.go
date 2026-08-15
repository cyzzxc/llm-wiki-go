package wiki

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// GraphParams parameterize graph building.
type GraphParams struct {
	Format     string
	Root       string
	Depth      int
	HasDepth   bool
	TypeFilter string
	Relation   string
	Output     string
	CrossWiki  bool
}

// GraphResult is the outcome of a graph build/render.
type GraphResult struct {
	Report   GraphReport
	Rendered string
}

// OpsGraphBuild builds and renders a concept graph.
func OpsGraphBuild(engine *WikiEngine, wikiName string, p GraphParams) (*GraphResult, error) {
	space, err := engine.Space(wikiName)
	if err != nil {
		return nil, err
	}
	format := p.Format
	if format == "" {
		format = space.Resolved.Graph.Format
	}

	var types []string
	for _, t := range splitComma(p.TypeFilter) {
		types = append(types, t)
	}
	filter := GraphFilter{
		Root:     p.Root,
		Depth:    p.Depth,
		HasDepth: p.HasDepth,
		Types:    types,
		Relation: p.Relation,
	}
	if p.HasDepth == false && p.Root == "" {
		filter.Depth = space.Resolved.Graph.Depth
	}

	var g *WikiGraph
	if p.CrossWiki {
		var wikis []NamedGraph
		for _, s := range engine.SpacesList() {
			wikis = append(wikis, NamedGraph{s.Name, mustGraph(s, GraphFilter{})})
		}
		g = MergeGraphs(wikis, filter)
	} else {
		g, err = space.GetOrBuildGraph(filter)
		if err != nil {
			return nil, err
		}
	}

	var rendered string
	switch format {
	case "dot":
		rendered = RenderDot(g)
	case "llms":
		rendered = RenderGraphLLMS(g)
	case "mermaid":
		rendered = RenderMermaid(g)
	default:
		return nil, fmt.Errorf("unknown graph format %q: expected mermaid, dot, or llms", format)
	}

	result := &GraphResult{
		Report:   GraphReport{Nodes: g.NodeCount(), Edges: g.EdgeCount(), Output: "stdout"},
		Rendered: rendered,
	}
	if p.Output != "" {
		out := p.Output
		if !filepath.IsAbs(out) {
			out = filepath.Join(space.WikiRoot, out)
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return nil, err
		}
		content := rendered
		if strings.HasSuffix(out, ".md") {
			content = WrapGraphMD(rendered, format, filter)
		}
		if err := os.WriteFile(out, []byte(content), 0o644); err != nil {
			return nil, err
		}
		result.Report.Output = out
	}
	return result, nil
}

func mustGraph(s *SpaceContext, filter GraphFilter) *WikiGraph {
	g, err := s.GetOrBuildGraph(filter)
	if err != nil {
		return NewWikiGraph()
	}
	return g
}

// ── Stats ────────────────────────────────────────────────────────────────────

// WikiStats is the wiki health dashboard.
type WikiStats struct {
	Wiki           string          `json:"wiki"`
	Pages          int             `json:"pages"`
	Sections       int             `json:"sections"`
	Types          map[string]int  `json:"types"`
	Status         map[string]int  `json:"status"`
	Orphans        int             `json:"orphans"`
	AvgConnections float64         `json:"avg_connections"`
	GraphDensity   float64         `json:"graph_density"`
	Staleness      StalenessCounts `json:"staleness"`
	Index          IndexHealth     `json:"index"`
	Communities    *CommunityStats `json:"communities"`
	Diameter       *float64        `json:"diameter"`
	Radius         *float64        `json:"radius"`
	Center         []string        `json:"center"`
	StructuralNote *string         `json:"structural_note"`
}

// StalenessCounts buckets pages by last_updated age.
type StalenessCounts struct {
	Fresh    int `json:"fresh"`
	Stale7d  int `json:"stale_7d"`
	Stale30d int `json:"stale_30d"`
}

// IndexHealth summarizes index state for stats.
type IndexHealth struct {
	Stale bool    `json:"stale"`
	Built *string `json:"built"`
}

// OpsStats computes the wiki health dashboard.
func OpsStats(engine *WikiEngine, wikiName string) (*WikiStats, error) {
	space, err := engine.Space(wikiName)
	if err != nil {
		return nil, err
	}
	ix := space.IndexManager.Searcher()

	stats := &WikiStats{
		Wiki:      wikiName,
		Types:     map[string]int{},
		Status:    map[string]int{},
		Staleness: StalenessCounts{},
	}
	if ix != nil {
		now := time.Now()
		for _, d := range ix.Docs {
			if d.Type == "section" {
				stats.Sections++
			} else {
				stats.Pages++
			}
			if d.Type != "" {
				stats.Types[d.Type]++
			}
			if d.Status != "" {
				stats.Status[d.Status]++
			}
			switch ageClass(d.TextVals["last_updated"], now) {
			case 0:
				stats.Staleness.Fresh++
			case 1:
				stats.Staleness.Stale7d++
			default:
				stats.Staleness.Stale30d++
			}
		}
	}

	if g, err := space.GetOrBuildGraph(GraphFilter{}); err == nil {
		m := ComputeMetrics(g)
		stats.Orphans = m.Orphans
		stats.AvgConnections = m.AvgConnections
		stats.GraphDensity = m.Density

		if cs, _ := space.CommunityData(space.Resolved.Graph.MinNodesForCommunities); cs != nil {
			stats.Communities = cs
		}

		if space.Resolved.Graph.StructuralAlgorithms {
			localCount := 0
			for _, n := range g.Nodes {
				if !n.External {
					localCount++
				}
			}
			if localCount <= space.Resolved.Graph.MaxNodesForDiameter {
				diameter, radius, center, _ := StructuralSummary(g)
				d, r := round2(diameter), round2(radius)
				stats.Diameter, stats.Radius, stats.Center = &d, &r, center
			} else {
				note := fmt.Sprintf("graph too large for diameter computation (%d nodes > max_nodes_for_diameter=%d)", localCount, space.Resolved.Graph.MaxNodesForDiameter)
				stats.StructuralNote = &note
			}
		}
	}

	status := space.IndexManager.Status(space.RepoRoot)
	stats.Index = IndexHealth{Stale: status.Stale, Built: status.Built}
	return stats, nil
}

func ageClass(dateStr string, now time.Time) int {
	if dateStr == "" {
		return 2
	}
	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return 2
	}
	days := now.Sub(t).Hours() / 24
	switch {
	case days < 7:
		return 0
	case days < 30:
		return 1
	default:
		return 2
	}
}

// ── Lint ─────────────────────────────────────────────────────────────────────

// LintFinding is a single lint result.
type LintFinding struct {
	Slug     string `json:"slug"`
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Path     string `json:"path"`
}

// LintReport aggregates lint findings.
type LintReport struct {
	Wiki     string        `json:"wiki"`
	Total    int           `json:"total"`
	Errors   int           `json:"errors"`
	Warnings int           `json:"warnings"`
	Findings []LintFinding `json:"findings"`
}

var allLintRules = []string{
	"orphan", "broken-link", "broken-cross-wiki-link", "missing-fields",
	"stale", "unknown-type", "articulation-point", "bridge", "periphery",
}

// OpsLint runs deterministic lint rules over the wiki index.
func OpsLint(engine *WikiEngine, wikiName, rulesArg, severityFilter string) (*LintReport, error) {
	space, err := engine.Space(wikiName)
	if err != nil {
		return nil, err
	}
	rules := allLintRules
	if rulesArg != "" {
		rules = splitComma(rulesArg)
	}
	ruleSet := map[string]bool{}
	for _, r := range rules {
		ruleSet[r] = true
	}

	report := &LintReport{Wiki: wikiName, Findings: []LintFinding{}}
	ix := space.IndexManager.Searcher()
	if ix == nil {
		return report, nil
	}

	slugPath := func(slug string) string {
		if p, err := mustSlug(slug).Resolve(space.WikiRoot); err == nil {
			return p
		}
		return filepath.Join(space.WikiRoot, slug+".md")
	}
	add := func(slug, rule, severity, message string) {
		if severityFilter != "" && severityFilter != severity {
			return
		}
		report.Findings = append(report.Findings, LintFinding{
			Slug: slug, Rule: rule, Severity: severity, Message: message, Path: slugPath(slug),
		})
	}

	// link field union for orphan detection
	linkedSlugs := map[string]bool{}
	for _, d := range ix.Docs {
		for _, l := range d.BodyLinks {
			linkedSlugs[l] = true
		}
		for _, field := range []string{"sources", "concepts", "document_refs"} {
			for _, l := range d.Fields[field] {
				linkedSlugs[l] = true
			}
		}
		if sb := firstOrEmpty(d.Fields["superseded_by"]); sb != "" {
			linkedSlugs[sb] = true
		}
	}

	for _, d := range ix.Docs {
		if ruleSet["orphan"] {
			isSection := d.Type == "section" || d.Slug == "index" || strings.HasSuffix(d.Slug, "/index")
			if !isSection && !linkedSlugs[d.Slug] {
				add(d.Slug, "orphan", "warning", "no incoming links")
			}
		}
		if ruleSet["broken-link"] || ruleSet["broken-cross-wiki-link"] {
			checkLinks := func(field string, targets []string) {
				for _, target := range targets {
					if strings.HasPrefix(target, "wiki://") {
						if ruleSet["broken-cross-wiki-link"] {
							pl := ParseParsedLink(target)
							if pl.CrossWiki {
								if _, err := engine.Space(pl.Wiki); err != nil {
									add(d.Slug, "broken-cross-wiki-link", "warning", fmt.Sprintf("cross-wiki link to unmounted wiki: %s", target))
								}
							}
						}
						continue
					}
					if ruleSet["broken-link"] && ix.Doc(target) == nil {
						add(d.Slug, "broken-link", "error", fmt.Sprintf("broken link in %s: %s", field, target))
					}
				}
			}
			checkLinks("body_links", d.BodyLinks)
			checkLinks("sources", d.Fields["sources"])
			checkLinks("concepts", d.Fields["concepts"])
			checkLinks("document_refs", d.Fields["document_refs"])
			if sb := firstOrEmpty(d.Fields["superseded_by"]); sb != "" {
				checkLinks("superseded_by", []string{sb})
			}
		}
		if ruleSet["missing-fields"] {
			if t, ok := space.TypeRegistry.Types[d.Type]; ok && d.Type != "" {
				for _, req := range t.RequiredFields {
					if !fieldPresentInDoc(d, req, space.IndexSchema) {
						add(d.Slug, "missing-fields", "error", fmt.Sprintf("required field missing: %s", req))
					}
				}
			}
		}
		if ruleSet["stale"] {
			if d.Status == "active" || d.Status == "" {
				stale := false
				note := "no last_updated date"
				if lu := d.TextVals["last_updated"]; lu != "" {
					if t, err := time.Parse("2006-01-02", lu); err == nil {
						days := time.Since(t).Hours() / 24
						if days > float64(space.Resolved.Lint.StaleDays) {
							stale = true
							note = fmt.Sprintf("last updated %s", lu)
						}
					} else {
						stale = true
					}
				} else {
					stale = true
				}
				if stale && d.Confidence != nil && *d.Confidence < space.Resolved.Lint.StaleConfidenceThreshold {
					add(d.Slug, "stale", "warning", fmt.Sprintf("stale page: %s", note))
				}
			}
		}
		if ruleSet["unknown-type"] {
			if d.Type != "" && !space.TypeRegistry.Has(d.Type) {
				add(d.Slug, "unknown-type", "error", fmt.Sprintf("unknown type: %s", d.Type))
			}
		}
	}

	// structural rules over the undirected local graph
	if ruleSet["articulation-point"] || ruleSet["bridge"] || ruleSet["periphery"] {
		g, err := space.GetOrBuildGraph(GraphFilter{})
		if err == nil {
			if ruleSet["articulation-point"] {
				for _, slug := range ArticulationPoints(g) {
					add(slug, "articulation-point", "warning", "removing this page would disconnect the graph — add alternative link paths")
				}
			}
			if ruleSet["bridge"] {
				for _, pair := range Bridges(g) {
					add(pair[0], "bridge", "warning", fmt.Sprintf("link %s → %s is a bridge — its removal disconnects the graph", pair[0], pair[1]))
				}
			}
			if ruleSet["periphery"] {
				localCount := 0
				for _, n := range g.Nodes {
					if !n.External {
						localCount++
					}
				}
				if localCount <= space.Resolved.Graph.MaxNodesForDiameter {
					_, _, _, periphery := StructuralSummary(g)
					for _, slug := range periphery {
						add(slug, "periphery", "warning", "most structurally isolated page — furthest from all others in the graph")
					}
				}
			}
		}
	}

	sort.SliceStable(report.Findings, func(i, j int) bool {
		if report.Findings[i].Slug != report.Findings[j].Slug {
			return report.Findings[i].Slug < report.Findings[j].Slug
		}
		return report.Findings[i].Rule < report.Findings[j].Rule
	})
	for _, f := range report.Findings {
		if f.Severity == "error" {
			report.Errors++
		} else {
			report.Warnings++
		}
	}
	report.Total = len(report.Findings)
	return report, nil
}

func firstOrEmpty(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	return ss[0]
}

func fieldPresentInDoc(d *IndexDoc, field string, is *IndexSchema) bool {
	// fields not in the index schema are treated as present
	if !is.HasField(field) {
		return true
	}
	switch field {
	case "title":
		return d.Title != ""
	case "type":
		return d.Type != ""
	case "status":
		return d.Status != ""
	case "tags":
		return d.Tags != nil
	case "confidence":
		return d.Confidence != nil
	case "body":
		return true
	}
	if _, ok := d.Fields[field]; ok {
		return true
	}
	if _, ok := d.TextVals[field]; ok {
		return true
	}
	_, ok := d.NumericVals[field]
	return ok
}

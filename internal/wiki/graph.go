package wiki

import (
	"compress/gzip"
	"encoding/gob"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// PageNode is one wiki page in the concept graph.
type PageNode struct {
	Slug     string
	Title    string
	Type     string
	External bool
}

// LabeledEdge is a directed edge with a relation label.
type LabeledEdge struct {
	Relation string
}

// Edge joins node indices in WikiGraph.
type Edge struct {
	From, To int
	LabeledEdge
}

// WikiGraph is the directed concept graph.
type WikiGraph struct {
	Nodes []PageNode
	Edges []Edge

	adjOut map[int][]int // node -> outgoing neighbor edge indices
	adjIn  map[int][]int
}

// NewWikiGraph creates an empty graph.
func NewWikiGraph() *WikiGraph {
	return &WikiGraph{adjOut: map[int][]int{}, adjIn: map[int][]int{}}
}

// AddNode appends a node and returns its index.
func (g *WikiGraph) AddNode(n PageNode) int {
	g.Nodes = append(g.Nodes, n)
	return len(g.Nodes) - 1
}

// AddEdge appends a directed edge.
func (g *WikiGraph) AddEdge(from, to int, label LabeledEdge) {
	g.Edges = append(g.Edges, Edge{from, to, label})
	g.adjOut[from] = append(g.adjOut[from], len(g.Edges)-1)
	g.adjIn[to] = append(g.adjIn[to], len(g.Edges)-1)
}

// NodeCount returns the number of nodes.
func (g *WikiGraph) NodeCount() int { return len(g.Nodes) }

// EdgeCount returns the number of edges.
func (g *WikiGraph) EdgeCount() int { return len(g.Edges) }

// OutNeighbors returns nodes reachable via outgoing edges (with multiplicity).
func (g *WikiGraph) OutNeighbors(n int) []int {
	var out []int
	for _, ei := range g.adjOut[n] {
		out = append(out, g.Edges[ei].To)
	}
	return out
}

// InNeighbors returns nodes with incoming edges from n's perspective.
func (g *WikiGraph) InNeighbors(n int) []int {
	var out []int
	for _, ei := range g.adjIn[n] {
		out = append(out, g.Edges[ei].From)
	}
	return out
}

// GraphFilter parameterizes graph construction and subgraph extraction.
type GraphFilter struct {
	Root     string
	Depth    int
	HasDepth bool
	Types    []string
	Relation string
}

// IsDefault reports an unfiltered full-graph request.
func (f GraphFilter) IsDefault() bool {
	return f.Root == "" && len(f.Types) == 0 && f.Relation == ""
}

// GraphReport summarizes a graph build/render.
type GraphReport struct {
	Nodes  int    `json:"nodes"`
	Edges  int    `json:"edges"`
	Output string `json:"output"`
}

// GraphMetrics are the health metrics of a built graph.
type GraphMetrics struct {
	Nodes          int     `json:"nodes"`
	Edges          int     `json:"edges"`
	Orphans        int     `json:"orphans"`
	AvgConnections float64 `json:"avg_connections"`
	Density        float64 `json:"density"`
}

// ComputeMetrics computes graph health metrics.
func ComputeMetrics(g *WikiGraph) GraphMetrics {
	nodes, edges := g.NodeCount(), g.EdgeCount()
	orphans := 0
	for i := range g.Nodes {
		if len(g.adjOut[i]) == 0 && len(g.adjIn[i]) == 0 {
			orphans++
		}
	}
	var avg, density float64
	if nodes > 0 {
		avg = float64(edges) * 2 / float64(nodes)
	}
	if nodes > 1 {
		density = float64(edges) / (float64(nodes) * (float64(nodes) - 1))
	}
	return GraphMetrics{Nodes: nodes, Edges: edges, Orphans: orphans, AvgConnections: round2(avg), Density: round2(density)}
}

// BuildGraph constructs the concept graph from a search index. Edge
// relations come from x-graph-edges declarations; body wikilinks get the
// generic "links-to" relation.
func BuildGraph(ix *SearchIndex, filter GraphFilter, registry *TypeRegistry) *WikiGraph {
	typeSet := map[string]bool{}
	for _, t := range filter.Types {
		typeSet[t] = true
	}

	g := NewWikiGraph()
	slugToIdx := map[string]int{}

	type docInfo struct {
		slug, pageType string
		bodyLinks      []string
		edgeFields     []struct {
			field   string
			targets []string
		}
	}
	var allDocs []docInfo

	// nodes (sorted: docs are slug-sorted at build time)
	for _, d := range ix.Docs {
		if len(typeSet) != 0 && !typeSet[d.Type] {
			continue
		}
		g.AddNode(PageNode{Slug: d.Slug, Title: d.Title, Type: d.Type})
		slugToIdx[d.Slug] = len(g.Nodes) - 1

		info := docInfo{slug: d.Slug, pageType: d.Type, bodyLinks: d.BodyLinks}
		for _, decl := range registry.Edges(d.Type) {
			if targets := d.Fields[decl.Field]; len(targets) > 0 {
				info.edgeFields = append(info.edgeFields, struct {
					field   string
					targets []string
				}{decl.Field, targets})
			}
		}
		allDocs = append(allDocs, info)
	}

	// edges
	for _, di := range allDocs {
		from, ok := slugToIdx[di.slug]
		if !ok {
			continue
		}
		edgeDecls := registry.Edges(di.pageType)
		for _, ef := range di.edgeFields {
			relation := "links-to"
			for _, d := range edgeDecls {
				if d.Field == ef.field {
					relation = d.Relation
					break
				}
			}
			if filter.Relation != "" && filter.Relation != relation {
				continue
			}
			for _, target := range ef.targets {
				if to, ok := resolveOrExternal(target, g, slugToIdx); ok && from != to {
					g.AddEdge(from, to, LabeledEdge{Relation: relation})
				}
			}
		}
		if filter.Relation == "" || filter.Relation == "links-to" {
			for _, target := range di.bodyLinks {
				if to, ok := resolveOrExternal(target, g, slugToIdx); ok && from != to {
					g.AddEdge(from, to, LabeledEdge{Relation: "links-to"})
				}
			}
		}
	}

	if filter.Root != "" {
		depth := 3
		if filter.HasDepth {
			depth = filter.Depth
		}
		return Subgraph(g, filter.Root, depth)
	}
	return g
}

func resolveOrExternal(target string, g *WikiGraph, slugToIdx map[string]int) (int, bool) {
	if strings.HasPrefix(target, "wiki://") {
		if idx, ok := slugToIdx[target]; ok {
			return idx, true
		}
		pl := ParseParsedLink(target)
		idx := g.AddNode(PageNode{Slug: pl.Slug, Title: target, Type: "external", External: true})
		slugToIdx[target] = idx
		return idx, true
	}
	idx, ok := slugToIdx[target]
	return idx, ok
}

// NamedGraph pairs a wiki name with its graph for cross-wiki merges.
type NamedGraph struct {
	Name  string
	Graph *WikiGraph
}

// MergeGraphs merges per-wiki graphs into a cross-wiki graph with
// "wikiname/slug" node keys, resolving cross-wiki placeholders.
func MergeGraphs(wikis []NamedGraph, filter GraphFilter) *WikiGraph {
	merged := NewWikiGraph()
	globalIdx := map[string]int{}

	typeSet := map[string]bool{}
	for _, t := range filter.Types {
		typeSet[t] = true
	}

	for _, w := range wikis {
		for _, n := range w.Graph.Nodes {
			if n.External || (len(typeSet) != 0 && !typeSet[n.Type]) {
				continue
			}
			key := w.Name + "/" + n.Slug
			globalIdx[key] = merged.AddNode(PageNode{Slug: key, Title: n.Title, Type: n.Type})
		}
	}
	for _, w := range wikis {
		for _, e := range w.Graph.Edges {
			fromNode, toNode := w.Graph.Nodes[e.From], w.Graph.Nodes[e.To]
			if fromNode.External {
				continue
			}
			if filter.Relation != "" && e.Relation != filter.Relation {
				continue
			}
			fromKey := w.Name + "/" + fromNode.Slug
			fromM, ok := globalIdx[fromKey]
			if !ok {
				continue
			}
			var toKey string
			if toNode.External {
				pl := ParseParsedLink(toNode.Title)
				if !pl.CrossWiki {
					continue
				}
				toKey = pl.Wiki + "/" + pl.Slug
			} else {
				toKey = w.Name + "/" + toNode.Slug
			}
			toM, ok := globalIdx[toKey]
			if !ok {
				toM = merged.AddNode(PageNode{Slug: toKey, Title: toNode.Title, Type: "external", External: true})
				globalIdx[toKey] = toM
			}
			if fromM != toM {
				merged.AddEdge(fromM, toM, LabeledEdge{Relation: e.Relation})
			}
		}
	}
	return merged
}

// Subgraph extracts a BFS subgraph rooted at rootSlug up to depth hops in
// both directions.
func Subgraph(g *WikiGraph, rootSlug string, depth int) *WikiGraph {
	rootIdx := -1
	for i, n := range g.Nodes {
		if n.Slug == rootSlug {
			rootIdx = i
			break
		}
	}
	if rootIdx < 0 {
		return NewWikiGraph()
	}
	visited := map[int]bool{rootIdx: true}
	queue := []struct {
		node  int
		depth int
	}{{rootIdx, 0}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth >= depth {
			continue
		}
		for _, nb := range append(g.OutNeighbors(cur.node), g.InNeighbors(cur.node)...) {
			if !visited[nb] {
				visited[nb] = true
				queue = append(queue, struct {
					node  int
					depth int
				}{nb, cur.depth + 1})
			}
		}
	}
	newGraph := NewWikiGraph()
	oldToNew := map[int]int{}
	for old := range visited {
		oldToNew[old] = newGraph.AddNode(g.Nodes[old])
	}
	for _, e := range g.Edges {
		if nf, ok := oldToNew[e.From]; ok {
			if nt, ok2 := oldToNew[e.To]; ok2 {
				newGraph.AddEdge(nf, nt, e.LabeledEdge)
			}
		}
	}
	return newGraph
}

// ── Louvain community detection (phase 1, deterministic) ─────────────────────

// CommunityStats aggregates community detection results.
type CommunityStats struct {
	Count    int      `json:"count"`
	Largest  int      `json:"largest"`
	Smallest int      `json:"smallest"`
	Isolated []string `json:"isolated"`
}

// BuildCommunityData runs Louvain phase 1 on the symmetrized local graph.
// minNodes gates the run (0 = always). Returns nil maps below threshold.
func BuildCommunityData(g *WikiGraph, minNodes int) (*CommunityStats, map[string]int) {
	var local []int
	for i, n := range g.Nodes {
		if !n.External {
			local = append(local, i)
		}
	}
	sort.Slice(local, func(i, j int) bool { return g.Nodes[local[i]].Slug < g.Nodes[local[j]].Slug })
	if len(local) < minNodes {
		return nil, nil
	}

	// undirected adjacency over local nodes
	adj := map[int]map[int]bool{}
	for _, n := range local {
		adj[n] = map[int]bool{}
	}
	for _, e := range g.Edges {
		if g.Nodes[e.From].External || g.Nodes[e.To].External {
			continue
		}
		adj[e.From][e.To] = true
		adj[e.To][e.From] = true
	}
	degrees := map[int]int{}
	m := 0
	for n, nbrs := range adj {
		degrees[n] = len(nbrs)
		m += len(nbrs)
	}
	m /= 2

	community := map[int]int{}
	for i, n := range local {
		community[n] = i
	}
	louvainPhase1(adj, community, degrees, m, local)

	// normalize to contiguous ids in local-slug order
	idRemap := map[int]int{}
	nextID := 0
	for _, n := range local {
		c := community[n]
		if _, ok := idRemap[c]; !ok {
			idRemap[c] = nextID
			nextID++
		}
	}
	communityMap := map[string]int{}
	sizes := map[int]int{}
	for _, n := range local {
		c := idRemap[community[n]]
		community[n] = c
		communityMap[g.Nodes[n].Slug] = c
		sizes[c]++
	}
	largest, smallest := 0, 0
	first := true
	for _, s := range sizes {
		if first || s > largest {
			largest = s
		}
		if first || s < smallest {
			smallest = s
		}
		first = false
	}
	var isolated []string
	for _, n := range local {
		if sizes[community[n]] <= 2 {
			isolated = append(isolated, g.Nodes[n].Slug)
		}
	}
	sort.Strings(isolated)
	return &CommunityStats{Count: nextID, Largest: largest, Smallest: smallest, Isolated: isolated}, communityMap
}

func louvainPhase1(adj map[int]map[int]bool, community map[int]int, degrees map[int]int, m int, order []int) bool {
	if m == 0 {
		return false
	}
	mF := float64(m)
	moved := false
	maxPasses := len(order)*10 + 100
	for pass := 0; pass < maxPasses; pass++ {
		anyMove := false
		for _, node := range order {
			currentC := community[node]
			kI := float64(degrees[node])

			neighborCEdges := map[int]int{}
			for nb := range adj[node] {
				neighborCEdges[community[nb]]++
			}
			sigmaTot := map[int]float64{}
			for n2, c2 := range community {
				if n2 == node {
					continue
				}
				sigmaTot[c2] += float64(degrees[n2])
			}

			bestC, bestGain := currentC, 0.0
			for c, kIn := range neighborCEdges {
				if c == currentC {
					continue
				}
				st := sigmaTot[c]
				gain := float64(kIn)/mF - st*kI/(2*mF*mF)
				if gain > bestGain {
					bestGain = gain
					bestC = c
				}
			}
			if bestC != currentC {
				community[node] = bestC
				anyMove = true
				moved = true
			}
		}
		if !anyMove {
			break
		}
	}
	return moved
}

// ── Structural topology (lint + stats) ───────────────────────────────────────

// UndirectedLocal builds an undirected adjacency of non-external nodes.
func UndirectedLocal(g *WikiGraph) (nodes []int, adj map[int][]int) {
	nodes = nil
	idxOf := map[int]int{}
	for i, n := range g.Nodes {
		if !n.External {
			idxOf[i] = len(nodes)
			nodes = append(nodes, i)
		}
	}
	adj = make(map[int][]int, len(nodes))
	set := map[int]map[int]bool{}
	for _, n := range nodes {
		set[n] = map[int]bool{}
	}
	for _, e := range g.Edges {
		if g.Nodes[e.From].External || g.Nodes[e.To].External {
			continue
		}
		set[e.From][e.To] = true
		set[e.To][e.From] = true
	}
	for n, nbrs := range set {
		for nb := range nbrs {
			adj[idxOf[n]] = append(adj[idxOf[n]], idxOf[nb])
		}
	}
	return nodes, adj
}

// ArticulationPoints returns local nodes whose removal disconnects the
// undirected graph (Tarjan).
func ArticulationPoints(g *WikiGraph) []string {
	nodes, adj := UndirectedLocal(g)
	n := len(nodes)
	visited := make([]bool, n)
	disc := make([]int, n)
	low := make([]int, n)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = -1
	}
	isAP := make([]bool, n)
	timer := 0
	var dfs func(u int)
	dfs = func(u int) {
		visited[u] = true
		disc[u], low[u] = timer, timer
		timer++
		children := 0
		for _, v := range adj[u] {
			if !visited[v] {
				children++
				parent[v] = u
				dfs(v)
				if low[v] < low[u] {
					low[u] = low[v]
				}
				if parent[u] == -1 && children > 1 {
					isAP[u] = true
				}
				if parent[u] != -1 && low[v] >= disc[u] {
					isAP[u] = true
				}
			} else if v != parent[u] && disc[v] < low[u] {
				low[u] = disc[v]
			}
		}
	}
	for i := 0; i < n; i++ {
		if !visited[i] {
			dfs(i)
		}
	}
	var out []string
	for i, ap := range isAP {
		if ap {
			out = append(out, g.Nodes[nodes[i]].Slug)
		}
	}
	sort.Strings(out)
	return out
}

// Bridges returns undirected edges (slug pairs) whose removal disconnects
// the graph (Tarjan).
func Bridges(g *WikiGraph) [][2]string {
	nodes, adj := UndirectedLocal(g)
	n := len(nodes)
	visited := make([]bool, n)
	disc := make([]int, n)
	low := make([]int, n)
	timer := 0
	var bridges [][2]string
	var dfs func(u, parent int)
	dfs = func(u, parent int) {
		visited[u] = true
		disc[u], low[u] = timer, timer
		timer++
		for _, v := range adj[u] {
			if !visited[v] {
				dfs(v, u)
				if low[v] < low[u] {
					low[u] = low[v]
				}
				if low[v] > disc[u] {
					bridges = append(bridges, [2]string{g.Nodes[nodes[u]].Slug, g.Nodes[nodes[v]].Slug})
				}
			} else if v != parent && disc[v] < low[u] {
				low[u] = disc[v]
			}
		}
	}
	for i := 0; i < n; i++ {
		if !visited[i] {
			dfs(i, -1)
		}
	}
	sort.Slice(bridges, func(i, j int) bool {
		if bridges[i][0] != bridges[j][0] {
			return bridges[i][0] < bridges[j][0]
		}
		return bridges[i][1] < bridges[j][1]
	})
	return bridges
}

// Eccentricity returns per-node eccentricity via BFS (undirected local).
func Eccentricity(g *WikiGraph) []int {
	nodes, adj := UndirectedLocal(g)
	n := len(nodes)
	ecc := make([]int, n)
	for s := 0; s < n; s++ {
		dist := make([]int, n)
		for i := range dist {
			dist[i] = -1
		}
		dist[s] = 0
		queue := []int{s}
		for len(queue) > 0 {
			u := queue[0]
			queue = queue[1:]
			for _, v := range adj[u] {
				if dist[v] < 0 {
					dist[v] = dist[u] + 1
					queue = append(queue, v)
				}
			}
		}
		max := 0
		for _, d := range dist {
			if d > max {
				max = d
			}
		}
		ecc[s] = max
	}
	return ecc
}

// StructuralSummary computes diameter, radius, center, and periphery over
// the local undirected graph.
func StructuralSummary(g *WikiGraph) (diameter float64, radius float64, center []string, periphery []string) {
	nodes, _ := UndirectedLocal(g)
	ecc := Eccentricity(g)
	if len(ecc) == 0 {
		return 0, 0, nil, nil
	}
	maxE, minE := 0, int(^uint(0)>>1)
	for _, e := range ecc {
		if e > maxE {
			maxE = e
		}
		if e < minE {
			minE = e
		}
	}
	for i, e := range ecc {
		if e == maxE {
			periphery = append(periphery, g.Nodes[nodes[i]].Slug)
		}
		if e == minE {
			center = append(center, g.Nodes[nodes[i]].Slug)
		}
	}
	sort.Strings(center)
	sort.Strings(periphery)
	return float64(maxE), float64(minE), center, periphery
}

// ── Renderers ────────────────────────────────────────────────────────────────

// RenderGraphLLMS renders a natural-language graph description.
func RenderGraphLLMS(g *WikiGraph) string {
	nodes, edges := g.NodeCount(), g.EdgeCount()

	var externalRefs []string
	byType := map[string][]string{}
	for _, n := range g.Nodes {
		if n.External {
			externalRefs = append(externalRefs, n.Title)
			continue
		}
		byType[n.Type] = append(byType[n.Type], n.Title)
	}
	type group struct {
		name   string
		titles []string
	}
	var groups []group
	for name, titles := range byType {
		groups = append(groups, group{name, titles})
	}
	sort.Slice(groups, func(i, j int) bool {
		if len(groups[i].titles) != len(groups[j].titles) {
			return len(groups[i].titles) > len(groups[j].titles)
		}
		return groups[i].name < groups[j].name
	})

	relationCounts := map[string]int{}
	for _, e := range g.Edges {
		relationCounts[e.Relation]++
	}
	var relations []struct {
		rel   string
		count int
	}
	for rel, count := range relationCounts {
		relations = append(relations, struct {
			rel   string
			count int
		}{rel, count})
	}
	sort.Slice(relations, func(i, j int) bool {
		if relations[i].count != relations[j].count {
			return relations[i].count > relations[j].count
		}
		return relations[i].rel < relations[j].rel
	})

	type degreeEntry struct {
		deg   int
		title string
	}
	degrees := make([]degreeEntry, 0, nodes)
	for i := range g.Nodes {
		degrees = append(degrees, degreeEntry{len(g.adjOut[i]) + len(g.adjIn[i]), g.Nodes[i].Title})
	}
	sort.Slice(degrees, func(i, j int) bool {
		if degrees[i].deg != degrees[j].deg {
			return degrees[i].deg > degrees[j].deg
		}
		return degrees[i].title < degrees[j].title
	})
	var topHubs []string
	for _, d := range degrees {
		if d.deg == 0 || len(topHubs) >= 5 {
			continue
		}
		topHubs = append(topHubs, fmt.Sprintf("%s (%d edges)", d.title, d.deg))
	}

	var isolated []string
	for i := range g.Nodes {
		if len(g.adjOut[i]) == 0 && len(g.adjIn[i]) == 0 {
			isolated = append(isolated, g.Nodes[i].Title)
		}
	}

	var out strings.Builder
	fmt.Fprintf(&out, "The wiki graph has %d nodes and %d edges across %d type groups.\n\n", nodes, edges, len(groups))

	for _, gr := range groups {
		sort.Strings(gr.titles)
		sample := strings.Join(gr.titles, ", ")
		if len(gr.titles) > 8 {
			sample = strings.Join(gr.titles[:8], ", ") + ", ..."
		}
		fmt.Fprintf(&out, "**%s** (%d nodes): %s\n", gr.name, len(gr.titles), sample)
	}
	if len(topHubs) > 0 {
		fmt.Fprintf(&out, "\nKey hubs: %s\n", strings.Join(topHubs, ", "))
	}
	if len(relations) > 0 {
		out.WriteString("\n**Edges by relation:**\n")
		for _, r := range relations {
			fmt.Fprintf(&out, "- `%s` (%d)\n", r.rel, r.count)
		}
	}
	if len(isolated) > 0 {
		fmt.Fprintf(&out, "\n**Isolated nodes (%d):** %s\n", len(isolated), strings.Join(isolated, ", "))
	}
	if len(externalRefs) > 0 {
		sort.Strings(externalRefs)
		fmt.Fprintf(&out, "\n**External references (%d):** %s\n", len(externalRefs), strings.Join(externalRefs, ", "))
	}
	return out.String()
}

// RenderMermaid renders the graph as a Mermaid graph LR diagram.
func RenderMermaid(g *WikiGraph) string {
	var out strings.Builder
	out.WriteString("graph LR\n")

	typesSeen := map[string]bool{}
	hasExternal := false
	for _, n := range g.Nodes {
		safeID := mermaidID(n.Title)
		if n.External {
			fmt.Fprintf(&out, "  %s[\"%s\"]:::external\n", safeID, n.Title)
			hasExternal = true
		} else {
			fmt.Fprintf(&out, "  %s[\"%s\"]:::%s\n", safeID, n.Title, n.Type)
			typesSeen[n.Type] = true
		}
	}
	out.WriteString("\n")
	for _, e := range g.Edges {
		fmt.Fprintf(&out, "  %s -->|%s| %s\n", mermaidID(g.Nodes[e.From].Title), e.Relation, mermaidID(g.Nodes[e.To].Title))
	}
	out.WriteString("\n")
	if hasExternal {
		out.WriteString("  classDef external fill:#eee,stroke:#999,stroke-dasharray:5 5\n")
	}
	typeColors := [][2]string{
		{"concept", "#cce5ff"}, {"query-result", "#cce5ff"},
		{"paper", "#d4edda"}, {"article", "#d4edda"}, {"documentation", "#d4edda"},
		{"skill", "#ffeeba"}, {"doc", "#e2e3e5"}, {"section", "#f8f9fa"},
	}
	for _, tc := range typeColors {
		if typesSeen[tc[0]] {
			fmt.Fprintf(&out, "  classDef %s fill:%s\n", tc[0], tc[1])
		}
	}
	return out.String()
}

func mermaidID(s string) string {
	s = strings.ReplaceAll(s, "://", "__")
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, ":", "_")
	return s
}

// RenderDot renders the graph as a Graphviz DOT digraph.
func RenderDot(g *WikiGraph) string {
	var out strings.Builder
	out.WriteString("digraph wiki {\n")
	for _, n := range g.Nodes {
		if n.External {
			fmt.Fprintf(&out, "  \"%s\" [label=\"%s\" type=\"external\" style=\"dashed\"];\n", n.Title, n.Title)
		} else {
			fmt.Fprintf(&out, "  \"%s\" [label=\"%s\" type=\"%s\"];\n", n.Slug, n.Title, n.Type)
		}
	}
	for _, e := range g.Edges {
		fromID, toID := g.Nodes[e.From].Slug, g.Nodes[e.To].Slug
		if g.Nodes[e.From].External {
			fromID = g.Nodes[e.From].Title
		}
		if g.Nodes[e.To].External {
			toID = g.Nodes[e.To].Title
		}
		fmt.Fprintf(&out, "  \"%s\" -> \"%s\" [label=\"%s\"];\n", fromID, toID, e.Relation)
	}
	out.WriteString("}\n")
	return out.String()
}

// WrapGraphMD wraps rendered output in frontmatter + code fence markdown.
func WrapGraphMD(rendered, format string, filter GraphFilter) string {
	now := time.Now().UTC().Format(time.RFC3339)
	root := filter.Root
	depth := 0
	if filter.HasDepth {
		depth = filter.Depth
	}
	types := "[]"
	if len(filter.Types) > 0 {
		types = fmt.Sprintf("[%s]", strings.Join(filter.Types, ", "))
	}
	var out strings.Builder
	out.WriteString("---\n")
	out.WriteString("title: \"Wiki Graph\"\n")
	fmt.Fprintf(&out, "generated: \"%s\"\n", now)
	fmt.Fprintf(&out, "format: %s\n", format)
	fmt.Fprintf(&out, "root: %s\n", root)
	fmt.Fprintf(&out, "depth: %d\n", depth)
	fmt.Fprintf(&out, "types: %s\n", types)
	out.WriteString("status: generated\n")
	out.WriteString("---\n\n")
	fmt.Fprintf(&out, "```%s\n", format)
	out.WriteString(rendered)
	out.WriteString("```\n")
	return out.String()
}

// ── Graph cache with snapshot warm-start ─────────────────────────────────────

// GraphCache caches the full graph keyed by index generation, optionally
// snapshot-backed (gob, gzip for compressed format names).
type GraphCache struct {
	mu           sync.Mutex
	generation   uint64
	graph        *WikiGraph
	snapshotDir  string
	keep         int
	compress     bool
	lastSnapshot string
}

// NewGraphCache creates a cache. snapshotDir != "" enables snapshots.
func NewGraphCache(snapshotDir string, keep int, compressed bool) *GraphCache {
	return &GraphCache{snapshotDir: snapshotDir, keep: keep, compress: compressed}
}

// GetFresh returns the cached graph for gen, rebuilding via build on miss.
func (c *GraphCache) GetFresh(gen uint64, build func() (*WikiGraph, error)) (*WikiGraph, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.graph != nil && c.generation == gen {
		return c.graph, nil
	}
	g, err := build()
	if err != nil {
		return nil, err
	}
	c.graph, c.generation = g, gen
	if c.snapshotDir != "" {
		c.writeSnapshot(g)
	}
	return g, nil
}

// Rebuild forces a fresh build, invalidating the cache.
func (c *GraphCache) Rebuild(gen uint64, build func() (*WikiGraph, error)) (*WikiGraph, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	g, err := build()
	if err != nil {
		return nil, err
	}
	c.graph, c.generation = g, gen
	if c.snapshotDir != "" {
		c.writeSnapshot(g)
	}
	return g, nil
}

// WarmStart loads the newest snapshot into the cache without a build; the
// generation still forces a rebuild check on the next GetFresh.
func (c *GraphCache) WarmStart() {
	if c.snapshotDir == "" {
		return
	}
	entries, err := os.ReadDir(c.snapshotDir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "wiki-graph-") && strings.HasSuffix(e.Name(), ".gob") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return
	}
	sort.Strings(names) // UnixNano names sort chronologically
	newest := names[len(names)-1]
	f, err := os.Open(filepath.Join(c.snapshotDir, newest))
	if err != nil {
		return
	}
	defer f.Close()
	var g WikiGraph
	r := io.Reader(f)
	if c.compress {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return
		}
		defer gz.Close()
		r = gz
	}
	if err := gob.NewDecoder(r).Decode(&g); err != nil {
		return
	}
	g.adjOut = map[int][]int{}
	g.adjIn = map[int][]int{}
	for i, e := range g.Edges {
		g.adjOut[e.From] = append(g.adjOut[e.From], i)
		g.adjIn[e.To] = append(g.adjIn[e.To], i)
	}
	c.graph = &g
	c.lastSnapshot = newest
}

func (c *GraphCache) writeSnapshot(g *WikiGraph) {
	if err := os.MkdirAll(c.snapshotDir, 0o755); err != nil {
		return
	}
	path := filepath.Join(c.snapshotDir, fmt.Sprintf("wiki-graph-%d.gob", time.Now().UnixNano()))
	f, err := os.Create(path)
	if err != nil {
		return
	}
	var w io.WriteCloser = f
	if c.compress {
		gz := gzip.NewWriter(f)
		w = gz
	}
	if err := gob.NewEncoder(w).Encode(g); err != nil {
		w.Close()
		f.Close()
		os.Remove(path)
		return
	}
	w.Close()
	f.Close()
	c.lastSnapshot = filepath.Base(path)
	c.pruneSnapshots()
}

func (c *GraphCache) pruneSnapshots() {
	entries, err := os.ReadDir(c.snapshotDir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "wiki-graph-") && strings.HasSuffix(e.Name(), ".gob") {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names))) // newest first
	for i := c.keep; i < len(names); i++ {
		os.Remove(filepath.Join(c.snapshotDir, names[i]))
	}
}

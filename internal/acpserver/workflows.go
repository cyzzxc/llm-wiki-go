package acpserver

import (
	"fmt"
	"strings"

	"llm-wiki-go/internal/wiki"
)

// ── Workflow steps (mirroring the Rust ACP workflows) ────────────────────────

func (s *Server) stepSearch(sessionID, workflow, query, wikiName string, topK int) []wiki.PageRef {
	toolID := makeToolID(workflow, "search")
	s.sendToolCall(sessionID, toolID, fmt.Sprintf("wiki_search: %s", query), "search")

	result, err := wiki.OpsSearch(s.Engine, wikiName, wiki.SearchParams{Query: query, TopK: topK})
	if err != nil {
		s.sendToolResult(sessionID, toolID, "failed", err.Error())
		return nil
	}
	s.sendToolResult(sessionID, toolID, "completed", fmt.Sprintf("%d results", len(result.Results)))
	return result.Results
}

func (s *Server) stepRead(sessionID, workflow, slug, wikiName string, streamContent bool) {
	toolID := makeToolID(workflow, "read")
	s.sendToolCall(sessionID, toolID, fmt.Sprintf("wiki_content_read: %s", slug), "read")

	result, err := wiki.ContentRead(s.Engine, slug, wikiName, false, false)
	if err != nil {
		s.sendToolResult(sessionID, toolID, "failed", err.Error())
		return
	}
	if result.Kind == wiki.ContentPage {
		s.sendToolResult(sessionID, toolID, "completed", "")
		if streamContent {
			s.sendText(sessionID, result.Content)
		}
		return
	}
	s.sendToolResult(sessionID, toolID, "completed", "")
}

func (s *Server) stepReportResults(sessionID string, results []wiki.PageRef, wikiName string) {
	if len(results) == 0 {
		return
	}
	var hits []string
	for i, r := range results {
		if i >= 5 {
			break
		}
		hits = append(hits, fmt.Sprintf("- %s (score: %.2f)", r.URI, r.Score))
	}
	s.sendText(sessionID, fmt.Sprintf("Based on %d pages in %q:\n%s", len(results), wikiName, strings.Join(hits, "\n")))
}

// ── Workflows ────────────────────────────────────────────────────────────────

func (s *Server) runResearch(sessionID, query, wikiName string) {
	s.sendText(sessionID, fmt.Sprintf("Searching for: %s...", query))
	results := s.stepSearch(sessionID, "research", query, wikiName, 5)

	if s.isCancelled(sessionID) {
		s.sendText(sessionID, "Cancelled.")
		s.clearActiveRun(sessionID)
		return
	}
	if len(results) == 0 {
		s.sendText(sessionID, fmt.Sprintf("No results found for %q in wiki %q.", query, wikiName))
	} else {
		s.stepRead(sessionID, "research", results[0].Slug, wikiName, false)
		s.stepReportResults(sessionID, results, wikiName)
	}
	s.clearActiveRun(sessionID)
}

func (s *Server) runLint(sessionID, query, wikiName string) {
	toolID := makeToolID("lint", "lint")
	label := "wiki_lint"
	if query != "" {
		label = fmt.Sprintf("wiki_lint rules=%s", query)
	}
	s.sendToolCall(sessionID, toolID, label, "other")

	var rules *string
	if query != "" {
		rules = &query
	}
	report, err := wiki.OpsLint(s.Engine, wikiName, deref(rules), "")
	if err != nil {
		s.sendToolResult(sessionID, toolID, "failed", err.Error())
		s.clearActiveRun(sessionID)
		return
	}
	s.sendToolResult(sessionID, toolID, "completed",
		fmt.Sprintf("%d findings (%d errors, %d warnings)", report.Total, report.Errors, report.Warnings))
	for _, f := range report.Findings {
		if s.isCancelled(sessionID) {
			s.sendText(sessionID, "Cancelled.")
			return
		}
		s.sendText(sessionID, fmt.Sprintf("[%s] %s: %s", f.Severity, f.Slug, f.Message))
	}
	s.clearActiveRun(sessionID)
}

func (s *Server) runGraph(sessionID, query, wikiName string) {
	if s.isCancelled(sessionID) {
		s.sendText(sessionID, "Cancelled.")
		s.clearActiveRun(sessionID)
		return
	}
	toolID := makeToolID("graph", "graph")
	label := "wiki_graph"
	root := ""
	if query != "" {
		root = query
		label = fmt.Sprintf("wiki_graph root=%s", root)
	}
	s.sendToolCall(sessionID, toolID, label, "other")

	result, err := wiki.OpsGraphBuild(s.Engine, wikiName, wiki.GraphParams{Format: "llms", Root: root})
	if err != nil {
		s.sendToolResult(sessionID, toolID, "failed", err.Error())
		s.clearActiveRun(sessionID)
		return
	}
	s.sendToolResult(sessionID, toolID, "completed",
		fmt.Sprintf("Graph: %d nodes, %d edges", result.Report.Nodes, result.Report.Edges))
	s.sendText(sessionID, result.Rendered)
	s.clearActiveRun(sessionID)
}

func (s *Server) runIngest(sessionID, query, wikiName string) {
	if s.isCancelled(sessionID) {
		s.sendText(sessionID, "Cancelled.")
		s.clearActiveRun(sessionID)
		return
	}
	path := query
	if path == "" {
		path = s.sessionCwd()
	}
	toolID := makeToolID("ingest", "ingest")
	s.sendToolCall(sessionID, toolID, fmt.Sprintf("wiki_ingest: %s", path), "other")

	report, _, err := wiki.OpsIngest(s.Engine, wikiName, path, false, false)
	if err != nil {
		s.sendToolResult(sessionID, toolID, "failed", err.Error())
		s.clearActiveRun(sessionID)
		return
	}
	commitInfo := "no commit"
	if report.Commit != "" {
		c := report.Commit
		if len(c) > 8 {
			c = c[:8]
		}
		commitInfo = "commit " + c
	}
	s.sendToolResult(sessionID, toolID, "completed", fmt.Sprintf("%d pages validated, %d unchanged, %d warnings — %s",
		report.PagesValidated, report.UnchangedCount, len(report.Warnings), commitInfo))
	s.clearActiveRun(sessionID)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

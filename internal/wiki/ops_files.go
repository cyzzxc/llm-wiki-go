package wiki

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"llm-wiki-go/internal/assets"
)

// ── Ingest ops ───────────────────────────────────────────────────────────────

// OpsIngest validates, commits, and indexes files under a wiki path.
// Returns the report and (non-dry-run) the URIs of touched pages.
func OpsIngest(engine *WikiEngine, wikiName, path string, dryRun, redact bool) (*IngestReport, []string, error) {
	space, err := engine.Space(wikiName)
	if err != nil {
		return nil, nil, err
	}

	var changed map[string]bool
	if !dryRun {
		changed = map[string]bool{}
		wikiPrefix := relUnder(space.RepoRoot, space.WikiRoot)
		for p := range CollectChangedFiles(space.RepoRoot, space.WikiRoot, space.IndexManager.LastCommit()) {
			rel := strings.TrimPrefix(p, wikiPrefix+"/")
			changed[rel] = true
		}
	}
	opts := IngestOptions{
		DryRun:     dryRun,
		AutoCommit: space.Resolved.Ingest.AutoCommit,
	}
	if redact {
		cfg := space.Resolved.Redact
		opts.Redact = &cfg
	}
	if !dryRun {
		opts.ChangedPaths = changed
	}

	report, err := Ingest(path, opts, space.WikiRoot, space.RepoRoot, space.TypeRegistry, space.Resolved.Validation)
	if err != nil {
		return nil, nil, err
	}

	var notifyURIs []string
	if !dryRun {
		notifyURIs = CollectPageURIs(filepath.Join(space.WikiRoot, path), space.WikiRoot, space.Name)
		space.IndexManager.Update(space.WikiRoot, space.RepoRoot, space.IndexManager.LastCommit(), space.IndexSchema, space.TypeRegistry)
		report.Warnings = append(report.Warnings, validateEdgeTargets(space)...)
	}
	return report, notifyURIs, nil
}

// validateEdgeTargets warns when an edge target's type mismatches the
// declared target_types.
func validateEdgeTargets(space *SpaceContext) []string {
	ix := space.IndexManager.Searcher()
	if ix == nil {
		return nil
	}
	var warnings []string
	for _, d := range ix.Docs {
		for _, decl := range space.TypeRegistry.Edges(d.Type) {
			for _, target := range d.Fields[decl.Field] {
				td := ix.Doc(ParseParsedLink(target).Slug)
				if td == nil {
					continue
				}
				if len(decl.TargetTypes) == 0 {
					continue
				}
				ok := false
				for _, tt := range decl.TargetTypes {
					if tt == td.Type {
						ok = true
						break
					}
				}
				if !ok {
					warnings = append(warnings, fmt.Sprintf(
						"%s: edge '%s' target '%s' has type '%s', expected one of [%s]",
						d.Slug, decl.Relation, target, td.Type, strings.Join(decl.TargetTypes, ", ")))
				}
			}
		}
	}
	return warnings
}

// CollectPageURIs lists wiki:// URIs for all .md files under a path.
func CollectPageURIs(path, wikiRoot, wikiName string) []string {
	var uris []string
	walk := func(p string, isDir bool) {
		if isDir || !strings.HasSuffix(p, ".md") {
			return
		}
		if slug, err := SlugFromPath(p, wikiRoot); err == nil {
			uris = append(uris, fmt.Sprintf("wiki://%s/%s", wikiName, slug))
		}
	}
	if fileExists(path) {
		walk(path, false)
		return uris
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return uris
	}
	var stack []string
	for _, e := range entries {
		stack = append(stack, filepath.Join(path, e.Name()))
	}
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if st.IsDir() {
			sub, _ := os.ReadDir(p)
			for _, e := range sub {
				stack = append(stack, filepath.Join(p, e.Name()))
			}
		} else {
			walk(p, false)
		}
	}
	return uris
}

// ── History ops ──────────────────────────────────────────────────────────────

// HistoryResult is the page history output.
type HistoryResult struct {
	Slug    string         `json:"slug"`
	Entries []HistoryEntry `json:"entries"`
}

// OpsHistory returns git history for a page.
func OpsHistory(engine *WikiEngine, wikiName, uri string, limit int, follow *bool) (*HistoryResult, error) {
	space, slug, err := resolveUriTarget(engine, uri, wikiName)
	if err != nil {
		return nil, err
	}
	path, err := slug.Resolve(space.WikiRoot)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(space.RepoRoot, path)
	if err != nil {
		rel = path
	}
	if limit == 0 {
		limit = space.Resolved.History.DefaultLimit
	}
	useFollow := space.Resolved.History.Follow
	if follow != nil {
		useFollow = *follow
	}
	entries, err := GitPageHistory(space.RepoRoot, filepath.ToSlash(rel), limit, useFollow)
	if err != nil {
		return nil, err
	}
	return &HistoryResult{Slug: slug.String(), Entries: entries}, nil
}

// ── Export ops ───────────────────────────────────────────────────────────────

// ExportFormat selects an export flavor.
type ExportFormat string

// Export formats.
const (
	ExportLLMSTxt  ExportFormat = "llms-txt"
	ExportLLMSFull ExportFormat = "llms-full"
	ExportJSON     ExportFormat = "json"
)

// ParseExportFormat maps strings (defaulting to llms-txt).
func ParseExportFormat(s string) ExportFormat {
	switch s {
	case "llms-full":
		return ExportLLMSFull
	case "json":
		return ExportJSON
	default:
		return ExportLLMSTxt
	}
}

// ExportReport summarizes an export.
type ExportReport struct {
	Pages int    `json:"pages"`
	Bytes int    `json:"bytes"`
	Path  string `json:"path"`
}

type exportPage struct {
	slug       string
	uri        string
	title      string
	pageType   string
	status     string
	summary    string
	confidence *float64
}

// OpsExport exports the wiki to llms.txt / llms-full / json.
func OpsExport(engine *WikiEngine, wikiName, outPath string, format ExportFormat, includeArchived bool) (*ExportReport, error) {
	space, err := engine.Space(wikiName)
	if err != nil {
		return nil, err
	}
	ix := space.IndexManager.Searcher()
	if ix == nil {
		return nil, fmt.Errorf("index not built for wiki %q", wikiName)
	}

	var pages []exportPage
	for _, d := range ix.Docs {
		if !includeArchived && d.Status == "archived" {
			continue
		}
		pages = append(pages, exportPage{
			slug: d.Slug, uri: d.URI, title: d.Title, pageType: d.Type,
			status: d.Status, summary: d.Summary, confidence: d.Confidence,
		})
	}

	// sort: type count desc, type asc, confidence desc, title asc
	typeCount := map[string]int{}
	for _, p := range pages {
		typeCount[p.pageType]++
	}
	sort.SliceStable(pages, func(i, j int) bool {
		if typeCount[pages[i].pageType] != typeCount[pages[j].pageType] {
			return typeCount[pages[i].pageType] > typeCount[pages[j].pageType]
		}
		if pages[i].pageType != pages[j].pageType {
			return pages[i].pageType < pages[j].pageType
		}
		ci, cj := 1.0, 1.0
		if pages[i].confidence != nil {
			ci = *pages[i].confidence
		}
		if pages[j].confidence != nil {
			cj = *pages[j].confidence
		}
		if ci != cj {
			return ci > cj
		}
		return pages[i].title < pages[j].title
	})

	if outPath == "" {
		outPath = "llms.txt"
	}
	out := outPath
	if !filepath.IsAbs(out) {
		out = filepath.Join(space.WikiRoot, out)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return nil, err
	}

	var content string
	switch format {
	case ExportJSON:
		type jsonPage struct {
			Slug        string         `json:"slug"`
			URI         string         `json:"uri"`
			Title       string         `json:"title"`
			Type        string         `json:"type"`
			Status      string         `json:"status"`
			Confidence  *float64       `json:"confidence,omitempty"`
			Summary     string         `json:"summary"`
			Frontmatter map[string]any `json:"frontmatter,omitempty"`
			Body        *string        `json:"body,omitempty"`
		}
		var jsonPages []jsonPage
		for _, p := range pages {
			jp := jsonPage{Slug: p.slug, URI: p.uri, Title: p.title, Type: p.pageType, Status: p.status, Confidence: p.confidence, Summary: p.summary}
			raw, err := readPageRaw(p.slug, space.WikiRoot)
			if err == nil {
				page := ParseFrontmatter(raw)
				fm := Frontmatter{}
				for k, v := range page.Frontmatter {
					switch k {
					case "slug", "uri", "id", "title", "type", "status", "confidence", "summary", "body":
						continue
					}
					fm[k] = v
				}
				if len(fm) > 0 {
					jp.Frontmatter = fm
				}
				body := page.Body
				jp.Body = &body
			}
			jsonPages = append(jsonPages, jp)
		}
		buf, err := json.MarshalIndent(jsonPages, "", "  ")
		if err != nil {
			return nil, err
		}
		content = string(buf) + "\n"
	case ExportLLMSFull:
		var b strings.Builder
		fmt.Fprintf(&b, "# %s\n\n%d pages\n\n", space.Name, len(pages))
		for _, p := range pages {
			fmt.Fprintf(&b, "---\n\n# [%s](%s)\n\n", p.title, p.uri)
			if p.summary != "" {
				fmt.Fprintf(&b, "_%s_\n\n", p.summary)
			}
			if raw, err := readPageRaw(p.slug, space.WikiRoot); err == nil {
				b.WriteString(strings.TrimSpace(StripFrontmatter(raw)))
				b.WriteString("\n\n")
			}
		}
		content = b.String()
	default: // llms-txt
		var b strings.Builder
		fmt.Fprintf(&b, "# %s\n\n%d pages\n\n", space.Name, len(pages))
		byType := map[string][]exportPage{}
		var typeOrder []string
		for _, p := range pages {
			if _, ok := byType[p.pageType]; !ok {
				typeOrder = append(typeOrder, p.pageType)
			}
			byType[p.pageType] = append(byType[p.pageType], p)
		}
		sort.Slice(typeOrder, func(i, j int) bool {
			if len(byType[typeOrder[i]]) != len(byType[typeOrder[j]]) {
				return len(byType[typeOrder[i]]) > len(byType[typeOrder[j]])
			}
			return typeOrder[i] < typeOrder[j]
		})
		for _, t := range typeOrder {
			fmt.Fprintf(&b, "## %s (%d)\n\n", t, len(byType[t]))
			for _, p := range byType[t] {
				if p.summary == "" {
					fmt.Fprintf(&b, "- [%s](%s)\n", p.title, p.uri)
				} else {
					fmt.Fprintf(&b, "- [%s](%s): %s\n", p.title, p.uri, p.summary)
				}
			}
			b.WriteString("\n")
		}
		content = b.String()
	}

	if err := os.WriteFile(out, []byte(content), 0o644); err != nil {
		return nil, err
	}
	return &ExportReport{Pages: len(pages), Bytes: len(content), Path: out}, nil
}

func readPageRaw(slug, wikiRoot string) (string, error) {
	s, err := NewSlug(slug)
	if err != nil {
		return "", err
	}
	path, err := s.Resolve(wikiRoot)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// ── Schema ops ───────────────────────────────────────────────────────────────

// SchemaListEntry is one registered type.
type SchemaListEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	SchemaPath  string `json:"schema_path"`
}

// OpsSchemaList lists registered types sorted by name.
func OpsSchemaList(space *SpaceContext) []SchemaListEntry {
	var out []SchemaListEntry
	for name, t := range space.TypeRegistry.Types {
		out = append(out, SchemaListEntry{Name: name, Description: t.Description, SchemaPath: t.SchemaPath})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// OpsSchemaShow returns the raw schema file content for a type.
func OpsSchemaShow(space *SpaceContext, typeName string) (string, error) {
	t, ok := space.TypeRegistry.Types[typeName]
	if !ok {
		return "", fmt.Errorf("type '%s' is not registered", typeName)
	}
	if raw, err := os.ReadFile(t.SchemaPath); err == nil {
		return string(raw), nil
	}
	if raw := embeddedSchemaByPath(t.SchemaPath); raw != nil {
		return string(raw), nil
	}
	return "", fmt.Errorf("type '%s' is not registered", typeName)
}

func embeddedSchemaByPath(path string) []byte {
	base := filepath.Base(path)
	if raw := assets.Schema(base); raw != nil && strings.HasSuffix(base, ".json") {
		return raw
	}
	return nil
}

// OpsSchemaShowTemplate generates frontmatter from a type schema.
func OpsSchemaShowTemplate(space *SpaceContext, typeName string) (string, error) {
	t, ok := space.TypeRegistry.Types[typeName]
	if !ok {
		return "", fmt.Errorf("type '%s' is not registered", typeName)
	}
	today := time.Now().Format("2006-01-02")
	var b strings.Builder
	b.WriteString("---\n")
	written := map[string]bool{}
	for _, req := range t.RequiredFields {
		writeTemplateField(&b, req, typeName, today, written)
	}
	for _, opt := range []string{"summary", "status", "last_updated", "tags"} {
		writeTemplateField(&b, opt, typeName, today, written)
	}
	b.WriteString("---\n")
	return b.String(), nil
}

func writeTemplateField(b *strings.Builder, name, typeName, today string, written map[string]bool) {
	if written[name] {
		return
	}
	switch name {
	case "type":
		fmt.Fprintf(b, "type: %s\n", typeName)
	case "status":
		b.WriteString("status: active\n")
	case "last_updated":
		fmt.Fprintf(b, "last_updated: \"%s\"\n", today)
	case "name":
		b.WriteString("name: \"\"\n")
	case "description":
		b.WriteString("description: \"\"\n")
	case "tags", "read_when":
		if name == "read_when" || name == "tags" {
			fmt.Fprintf(b, "%s:\n  - \"\"\n", name)
		}
	default:
		fmt.Fprintf(b, "%s: \"\"\n", name)
	}
	written[name] = true
}

// OpsSchemaValidate validates schema files and index resolution.
func OpsSchemaValidate(space *SpaceContext) []string {
	var issues []string
	schemasDir := filepath.Join(space.RepoRoot, "schemas")
	entries, err := os.ReadDir(schemasDir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			p := filepath.Join(schemasDir, e.Name())
			raw, err := os.ReadFile(p)
			if err != nil {
				issues = append(issues, fmt.Sprintf("%s: cannot read", e.Name()))
				continue
			}
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				issues = append(issues, fmt.Sprintf("%s: invalid JSON: %v", e.Name(), err))
				continue
			}
			if _, ok := doc["x-wiki-types"]; !ok {
				issues = append(issues, fmt.Sprintf("%s: missing x-wiki-types (types won't be discovered)", e.Name()))
			}
		}
	}
	return issues
}

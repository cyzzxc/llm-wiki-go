// Package mcpserver exposes the wiki engine over the Model Context
// Protocol: 23 wiki_* tools, wiki:// resources, stdio and Streamable HTTP
// transports.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"llm-wiki-go/internal/wiki"
)

// Version is the server implementation version.
const Version = "0.1.0"

// Server wraps the engine and the MCP server instance.
type Server struct {
	Engine *wiki.WikiEngine
	MCP    *mcp.Server

	resMu      sync.Mutex
	registered map[string]bool
}

// arg helpers — matching the Rust "missing required parameter: {key}" convention.

type args map[string]any

func (a args) str(key string) string {
	s, _ := a[key].(string)
	return s
}

func (a args) strReq(key string) (string, error) {
	s, ok := a[key].(string)
	if !ok || s == "" {
		return "", fmt.Errorf("missing required parameter: %s", key)
	}
	return s, nil
}

func (a args) boolean(key string) bool {
	b, _ := a[key].(bool)
	return b
}

func (a args) intVal(key string) int {
	switch v := a[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

func (a args) hasInt(key string) bool {
	switch a[key].(type) {
	case float64, int:
		return true
	}
	return false
}

func (a args) boolPtr(key string) *bool {
	if b, ok := a[key].(bool); ok {
		return &b
	}
	return nil
}

// New builds the MCP server with all tools and resources registered.
func New(engine *wiki.WikiEngine) *Server {
	s := &Server{
		Engine:     engine,
		registered: map[string]bool{},
	}
	impl := &mcp.Implementation{Name: "llm-wiki", Version: Version}
	s.MCP = mcp.NewServer(impl, &mcp.ServerOptions{})
	s.registerTools()
	s.syncResources()
	return s
}

// ── Tool schema helpers ──────────────────────────────────────────────────────

func schema(props map[string]any, required ...string) json.RawMessage {
	if required == nil {
		required = []string{}
	}
	m := map[string]any{"type": "object", "properties": props, "required": required}
	raw, _ := json.Marshal(m)
	return raw
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}
func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func (s *Server) addTool(name, desc string, props map[string]any, required []string,
	handler func(ctx context.Context, ss *mcp.ServerSession, a args) (string, bool, error)) {
	s.MCP.AddTool(&mcp.Tool{
		Name:        name,
		Description: desc,
		InputSchema: schema(props, required...),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var a args
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &a); err != nil {
				return errText("error: invalid arguments"), nil
			}
		}
		text, isErr, err := handler(ctx, req.Session, a)
		if err != nil {
			return errText("error: " + err.Error()), nil
		}
		if isErr {
			return errText("error: " + text), nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, nil
	})
}

func errText(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}

func okJSON(v any) (string, bool, error) {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", false, err
	}
	return string(raw), false, nil
}

// ── Tool registration (23 tools) ─────────────────────────────────────────────

func (s *Server) registerTools() {
	e := s.Engine

	s.addTool("wiki_spaces_create", "Initialize a new wiki repository",
		map[string]any{
			"path":        strProp("Path to create the wiki at"),
			"name":        strProp("Wiki name — used in wiki:// URIs"),
			"description": strProp("Optional one-line description"),
			"force":       boolProp("Update space entry if name already exists"),
			"set_default": boolProp("Set as default wiki"),
			"wiki_root":   strProp("Content directory relative to repo root (default: \"wiki\")"),
		}, []string{"path", "name"},
		func(ctx context.Context, ss *mcp.ServerSession, a args) (string, bool, error) {
			path, err := a.strReq("path")
			if err != nil {
				return "", true, err
			}
			name, err := a.strReq("name")
			if err != nil {
				return "", true, err
			}
			report, err := wiki.OpsSpacesCreate(e, path, name, a.str("description"), a.boolean("force"), a.boolean("set_default"), a.str("wiki_root"))
			if err != nil {
				return "", true, err
			}
			s.syncResources()
			return okJSON(report)
		})

	s.addTool("wiki_spaces_register", "Register an existing wiki repository without creating files",
		map[string]any{
			"path":        strProp("Absolute path to the existing wiki repository"),
			"name":        strProp("Wiki name — used in wiki:// URIs"),
			"description": strProp("Optional one-line description"),
			"wiki_root":   strProp("Content directory (overrides wiki.toml; must already exist)"),
		}, []string{"path", "name"},
		func(ctx context.Context, ss *mcp.ServerSession, a args) (string, bool, error) {
			path, err := a.strReq("path")
			if err != nil {
				return "", true, err
			}
			name, err := a.strReq("name")
			if err != nil {
				return "", true, err
			}
			report, err := wiki.OpsSpacesRegister(e, path, name, a.str("description"), a.str("wiki_root"))
			if err != nil {
				return "", true, err
			}
			s.syncResources()
			return okJSON(report)
		})

	s.addTool("wiki_spaces_list", "List all registered wiki spaces",
		map[string]any{"name": strProp("Wiki name (omit for all)")}, nil,
		func(ctx context.Context, ss *mcp.ServerSession, a args) (string, bool, error) {
			name := a.str("name")
			var out []*wiki.SpaceContext
			for _, sp := range e.SpacesList() {
				if name == "" || sp.Name == name {
					out = append(out, sp)
				}
			}
			entries := []map[string]any{}
			config := e.State.Config
			for _, entry := range config.Wikis {
				if name != "" && entry.Name != name {
					continue
				}
				m := map[string]any{"name": entry.Name, "path": entry.Path}
				if entry.Description != nil {
					m["description"] = *entry.Description
				}
				if entry.Remote != nil {
					m["remote"] = *entry.Remote
				}
				entries = append(entries, m)
			}
			if entries == nil {
				entries = []map[string]any{}
			}
			_ = out
			return okJSON(entries)
		})

	s.addTool("wiki_spaces_remove", "Remove a wiki space",
		map[string]any{
			"name":   strProp("Wiki name to remove"),
			"delete": boolProp("Also delete the wiki directory from disk"),
		}, []string{"name"},
		func(ctx context.Context, ss *mcp.ServerSession, a args) (string, bool, error) {
			name, err := a.strReq("name")
			if err != nil {
				return "", true, err
			}
			if err := wiki.OpsSpacesRemove(e, name, a.boolean("delete")); err != nil {
				return "", true, err
			}
			s.syncResources()
			return fmt.Sprintf("Removed wiki %q", name), false, nil
		})

	s.addTool("wiki_spaces_set_default", "Set the default wiki space",
		map[string]any{"name": strProp("Wiki name to set as default")}, []string{"name"},
		func(ctx context.Context, ss *mcp.ServerSession, a args) (string, bool, error) {
			name, err := a.strReq("name")
			if err != nil {
				return "", true, err
			}
			if err := wiki.OpsSpacesSetDefault(e, name); err != nil {
				return "", true, err
			}
			s.syncResources()
			return fmt.Sprintf("Default wiki set to %q", name), false, nil
		})

	s.addTool("wiki_config", "Get or set configuration values",
		map[string]any{
			"action": strProp("Action: get, set, or list"),
			"key":    strProp("Config key (for get/set)"),
			"value":  strProp("Config value (for set)"),
			"global": boolProp("Write to global config"),
			"wiki":   strProp("Target wiki name"),
		}, []string{"action"},
		func(ctx context.Context, ss *mcp.ServerSession, a args) (string, bool, error) {
			switch action := a.str("action"); action {
			case "list":
				text, err := wiki.OpsConfigListGlobal(e)
				return text, false, err
			case "get":
				key, err := a.strReq("key")
				if err != nil {
					return "", true, err
				}
				val, err := wiki.OpsConfigGet(e, a.str("wiki"), key)
				if err != nil {
					return "", true, err
				}
				return fmt.Sprintf("%s: %s", key, val), false, nil
			case "set":
				key, err := a.strReq("key")
				if err != nil {
					return "", true, err
				}
				value, err := a.strReq("value")
				if err != nil {
					return "", true, err
				}
				text, err := wiki.OpsConfigSet(e, a.str("wiki"), key, value, a.boolean("global"))
				return text, false, err
			default:
				return "", true, fmt.Errorf("unknown config action: %s", action)
			}
		})

	s.addTool("wiki_content_read", "Read full content of a page by slug or URI",
		map[string]any{
			"uri":            strProp("Slug or wiki:// URI"),
			"no_frontmatter": boolProp("Strip frontmatter from output"),
			"list_assets":    boolProp("List co-located assets instead of content"),
			"backlinks":      boolProp("Include incoming links — pages that link to this page"),
			"wiki":           strProp("Target wiki name"),
		}, []string{"uri"},
		func(ctx context.Context, ss *mcp.ServerSession, a args) (string, bool, error) {
			uri, err := a.strReq("uri")
			if err != nil {
				return "", true, err
			}
			result, err := wiki.ContentRead(e, uri, a.str("wiki"), a.boolean("no_frontmatter"), a.boolean("list_assets"))
			if err != nil {
				return "", true, err
			}
			switch result.Kind {
			case wiki.ContentAssets:
				return strings.Join(result.Assets, "\n"), false, nil
			case wiki.ContentBinary:
				return "", true, fmt.Errorf("asset is binary — access it directly from the filesystem")
			default:
				if a.boolean("backlinks") {
					space, slug, err := resolveUri(e, uri, a.str("wiki"))
					if err != nil {
						return "", true, err
					}
					backlinks := wiki.BacklinksQuery(e, space.Name, slug.String())
					payload := map[string]any{"content": result.Content, "backlinks": backlinks}
					return okJSON(payload)
				}
				return result.Content, false, nil
			}
		})

	s.addTool("wiki_content_write", "Write content to a page in the wiki tree",
		map[string]any{
			"uri":     strProp("Slug or wiki:// URI"),
			"content": strProp("File content"),
			"wiki":    strProp("Target wiki name"),
		}, []string{"uri", "content"},
		func(ctx context.Context, ss *mcp.ServerSession, a args) (string, bool, error) {
			uri, err := a.strReq("uri")
			if err != nil {
				return "", true, err
			}
			content, err := a.strReq("content")
			if err != nil {
				return "", true, err
			}
			res, _, err := wiki.ContentWrite(e, uri, content, a.str("wiki"))
			if err != nil {
				return "", true, err
			}
			return fmt.Sprintf("Wrote %d bytes to %s", res.BytesWritten, res.Path), false, nil
		})

	s.addTool("wiki_content_new", "Create a page or section with scaffolded frontmatter",
		map[string]any{
			"uri":     strProp("Slug or wiki:// URI"),
			"section": boolProp("Create a section instead of a page"),
			"bundle":  boolProp("Create as bundle (folder + index.md)"),
			"name":    strProp("Page title (default: derived from slug)"),
			"type":    strProp("Page type (default: page)"),
			"wiki":    strProp("Target wiki name"),
		}, []string{"uri"},
		func(ctx context.Context, ss *mcp.ServerSession, a args) (string, bool, error) {
			uri, err := a.strReq("uri")
			if err != nil {
				return "", true, err
			}
			result, err := wiki.ContentNew(e, uri, a.str("wiki"), a.boolean("section"), a.boolean("bundle"), a.str("name"), a.str("type"))
			if err != nil {
				return "", true, err
			}
			return okJSON(result)
		})

	s.addTool("wiki_content_commit", "Commit pending changes to git",
		map[string]any{
			"slugs":   strProp("Comma-separated page slugs to commit (omit for all)"),
			"message": strProp("Commit message"),
			"wiki":    strProp("Target wiki name"),
		}, nil,
		func(ctx context.Context, ss *mcp.ServerSession, a args) (string, bool, error) {
			var slugs []string
			if raw := a.str("slugs"); raw != "" {
				for _, slug := range strings.Split(raw, ",") {
					if slug = strings.TrimSpace(slug); slug != "" {
						slugs = append(slugs, slug)
					}
				}
			}
			all := len(slugs) == 0
			hash, err := wiki.ContentCommit(e, a.str("wiki"), slugs, all, a.str("message"))
			if err != nil {
				return "", true, err
			}
			return hash, false, nil
		})

	s.addTool("wiki_search", "Full-text BM25 search, returns ranked results",
		map[string]any{
			"query":            strProp("Search query"),
			"type":             strProp("Filter by frontmatter type"),
			"no_excerpt":       boolProp("Omit excerpts — refs only"),
			"include_sections": boolProp("Include section index pages"),
			"top_k":            intProp("Max results"),
			"wiki":             strProp("Target wiki name"),
			"cross_wiki":       boolProp("Search across all wikis"),
			"mode":             strProp("Ranking mode: keyword (BM25, default) | semantic (embedding cosine — requires [embedding] config) | hybrid (blend)"),
			"format":           strProp("Output format: json | llms (default: json)"),
		}, []string{"query"},
		func(ctx context.Context, ss *mcp.ServerSession, a args) (string, bool, error) {
			query, err := a.strReq("query")
			if err != nil {
				return "", true, err
			}
			wikiName, err := s.resolveWikiName(a)
			if err != nil {
				return "", true, err
			}
			params := wiki.SearchParams{
				Query:           query,
				TypeFilter:      a.str("type"),
				NoExcerpt:       a.str("format") == "llms" || a.boolean("no_excerpt"),
				TopK:            a.intVal("top_k"),
				IncludeSections: a.boolean("include_sections"),
				CrossWiki:       a.boolean("cross_wiki"),
				Mode:            a.str("mode"),
			}
			result, err := wiki.OpsSearch(e, wikiName, params)
			if err != nil {
				return "", true, err
			}
			if a.str("format") == "llms" {
				return wiki.RenderSearchLLMS(result), false, nil
			}
			return okJSON(result)
		})

	s.addTool("wiki_list", "Paginated page listing with filters",
		map[string]any{
			"type":      strProp("Filter by frontmatter type"),
			"status":    strProp("Filter by frontmatter status"),
			"page":      intProp("Page number, 1-based"),
			"page_size": intProp("Results per page"),
			"wiki":      strProp("Target wiki name"),
			"format":    strProp("Output format: json | llms (default: json)"),
		}, nil,
		func(ctx context.Context, ss *mcp.ServerSession, a args) (string, bool, error) {
			wikiName, err := s.resolveWikiName(a)
			if err != nil {
				return "", true, err
			}
			result, _, err := wiki.OpsList(e, wikiName, a.str("type"), a.str("status"), a.intVal("page"), a.intVal("page_size"))
			if err != nil {
				return "", true, err
			}
			if a.str("format") == "llms" {
				return wiki.RenderListLLMS(result), false, nil
			}
			return okJSON(result)
		})

	s.addTool("wiki_ingest", "Validate, commit, and index files in the wiki tree",
		map[string]any{
			"path":    strProp("File or folder path, relative to wiki root"),
			"dry_run": boolProp("Show what would be created without creating"),
			"redact":  boolProp("Run redaction pass on file bodies before validation (opt-in; lossy — original values are replaced)"),
			"wiki":    strProp("Target wiki name"),
		}, []string{"path"},
		func(ctx context.Context, ss *mcp.ServerSession, a args) (string, bool, error) {
			path, err := a.strReq("path")
			if err != nil {
				return "", true, err
			}
			wikiName, err := s.resolveWikiName(a)
			if err != nil {
				return "", true, err
			}
			report, notifyURIs, err := wiki.OpsIngest(e, wikiName, path, a.boolean("dry_run"), a.boolean("redact"))
			if err != nil {
				return "", true, err
			}
			for _, uri := range notifyURIs {
				_ = s.MCP.ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: uri})
			}
			return okJSON(report)
		})

	s.addTool("wiki_index_rebuild", "Rebuild the search index",
		map[string]any{"wiki": strProp("Target wiki name")}, nil,
		func(ctx context.Context, ss *mcp.ServerSession, a args) (string, bool, error) {
			wikiName, err := s.resolveWikiName(a)
			if err != nil {
				return "", true, err
			}
			report, err := wiki.OpsIndexRebuild(e, wikiName)
			if err != nil {
				return "", true, err
			}
			return okJSON(report)
		})

	s.addTool("wiki_index_status", "Inspect index health",
		map[string]any{"wiki": strProp("Target wiki name")}, nil,
		func(ctx context.Context, ss *mcp.ServerSession, a args) (string, bool, error) {
			wikiName, err := s.resolveWikiName(a)
			if err != nil {
				return "", true, err
			}
			status, _, err := wiki.OpsIndexStatus(e, wikiName)
			if err != nil {
				return "", true, err
			}
			return okJSON(status)
		})

	s.addTool("wiki_graph", "Generate concept graph, returns GraphReport",
		map[string]any{
			"format":     strProp("Output format: mermaid | dot | llms (default: mermaid)"),
			"root":       strProp("Subgraph from this node (slug)"),
			"depth":      intProp("Hop limit from root"),
			"type":       strProp("Comma-separated page types to include"),
			"relation":   strProp("Filter edges by relation label"),
			"output":     strProp("File path for output (default: stdout/return)"),
			"cross_wiki": boolProp("Merge all mounted wikis into a unified graph"),
			"wiki":       strProp("Target wiki name"),
		}, nil,
		func(ctx context.Context, ss *mcp.ServerSession, a args) (string, bool, error) {
			wikiName, err := s.resolveWikiName(a)
			if err != nil {
				return "", true, err
			}
			p := wiki.GraphParams{
				Format:     a.str("format"),
				Root:       a.str("root"),
				Depth:      a.intVal("depth"),
				HasDepth:   a.hasInt("depth"),
				TypeFilter: a.str("type"),
				Relation:   a.str("relation"),
				Output:     a.str("output"),
				CrossWiki:  a.boolean("cross_wiki"),
			}
			result, err := wiki.OpsGraphBuild(e, wikiName, p)
			if err != nil {
				return "", true, err
			}
			return result.Rendered, false, nil
		})

	s.addTool("wiki_export", "Export the full wiki to a file (llms.txt, llms-full, or json)",
		map[string]any{
			"wiki":   strProp("Target wiki name"),
			"path":   strProp("Output path (relative to wiki root or absolute; default: llms.txt)"),
			"format": strProp("Export format: llms-txt | llms-full | json (default: llms-txt)"),
			"status": strProp("Page status filter: active | all (default: active, excludes archived)"),
		}, []string{"wiki"},
		func(ctx context.Context, ss *mcp.ServerSession, a args) (string, bool, error) {
			wikiName, err := a.strReq("wiki")
			if err != nil {
				return "", true, err
			}
			report, err := wiki.OpsExport(e, wikiName, a.str("path"), wiki.ParseExportFormat(a.str("format")), a.str("status") == "all")
			if err != nil {
				return "", true, err
			}
			return okJSON(report)
		})

	s.addTool("wiki_history", "Git commit history for a page",
		map[string]any{
			"slug":   strProp("Slug or wiki:// URI"),
			"limit":  intProp("Max entries to return"),
			"follow": boolProp("Track renames (default: from config)"),
			"wiki":   strProp("Target wiki name"),
		}, []string{"slug"},
		func(ctx context.Context, ss *mcp.ServerSession, a args) (string, bool, error) {
			slug, err := a.strReq("slug")
			if err != nil {
				return "", true, err
			}
			wikiName, err := s.resolveWikiName(a)
			if err != nil {
				return "", true, err
			}
			result, err := wiki.OpsHistory(e, wikiName, slug, a.intVal("limit"), a.boolPtr("follow"))
			if err != nil {
				return "", true, err
			}
			return okJSON(result)
		})

	s.addTool("wiki_stats", "Wiki health dashboard — page counts, graph metrics, staleness, structural topology (diameter, radius, center)",
		map[string]any{"wiki": strProp("Target wiki name")}, nil,
		func(ctx context.Context, ss *mcp.ServerSession, a args) (string, bool, error) {
			wikiName, err := s.resolveWikiName(a)
			if err != nil {
				return "", true, err
			}
			result, err := wiki.OpsStats(e, wikiName)
			if err != nil {
				return "", true, err
			}
			return okJSON(result)
		})

	s.addTool("wiki_suggest", "Suggest related pages to link",
		map[string]any{
			"slug":  strProp("Slug or wiki:// URI"),
			"limit": intProp("Max suggestions"),
			"wiki":  strProp("Target wiki name"),
		}, []string{"slug"},
		func(ctx context.Context, ss *mcp.ServerSession, a args) (string, bool, error) {
			slug, err := a.strReq("slug")
			if err != nil {
				return "", true, err
			}
			wikiName, err := s.resolveWikiName(a)
			if err != nil {
				return "", true, err
			}
			result, err := wiki.OpsSuggest(e, wikiName, slug, a.intVal("limit"))
			if err != nil {
				return "", true, err
			}
			return okJSON(result)
		})

	s.addTool("wiki_lint", "Run deterministic lint rules on the wiki index",
		map[string]any{
			"rules":    strProp("Comma-separated rule names: orphan, broken-link, broken-cross-wiki-link, missing-fields, stale, unknown-type, articulation-point, bridge, periphery (omit for all)"),
			"severity": strProp("Filter output: error | warning (omit for all)"),
			"wiki":     strProp("Target wiki name"),
		}, nil,
		func(ctx context.Context, ss *mcp.ServerSession, a args) (string, bool, error) {
			wikiName, err := s.resolveWikiName(a)
			if err != nil {
				return "", true, err
			}
			result, err := wiki.OpsLint(e, wikiName, a.str("rules"), a.str("severity"))
			if err != nil {
				return "", true, err
			}
			return okJSON(result)
		})

	s.addTool("wiki_resolve", "Resolve a slug or wiki:// URI to its local filesystem path. Use before writing content directly to disk.",
		map[string]any{
			"uri":  strProp("Slug or wiki:// URI"),
			"wiki": strProp("Target wiki name (optional, uses default)"),
		}, []string{"uri"},
		func(ctx context.Context, ss *mcp.ServerSession, a args) (string, bool, error) {
			uri, err := a.strReq("uri")
			if err != nil {
				return "", true, err
			}
			result, err := wiki.ResolveUriToPath(e, uri, a.str("wiki"))
			if err != nil {
				return "", true, err
			}
			return okJSON(result)
		})

	s.addTool("wiki_schema", "Inspect and manage type schemas",
		map[string]any{
			"action":       strProp("Action: list, show, add, remove, validate"),
			"type":         strProp("Type name (for show/add/remove/validate)"),
			"template":     boolProp("Return frontmatter template instead of schema (for show)"),
			"schema_path":  strProp("Path to schema file (for add)"),
			"delete":       boolProp("Also delete schema file (for remove)"),
			"delete_pages": boolProp("Also delete page files from disk (for remove)"),
			"dry_run":      boolProp("Show what would be done (for remove)"),
			"wiki":         strProp("Target wiki name"),
		}, []string{"action"},
		func(ctx context.Context, ss *mcp.ServerSession, a args) (string, bool, error) {
			wikiName, err := s.resolveWikiName(a)
			if err != nil {
				return "", true, err
			}
			space, err := e.Space(wikiName)
			if err != nil {
				return "", true, err
			}
			switch action := a.str("action"); action {
			case "list":
				return okJSON(wiki.OpsSchemaList(space))
			case "show":
				typeName := a.str("type")
				if typeName == "" {
					return "", true, fmt.Errorf("type is required for show")
				}
				if a.boolean("template") {
					text, err := wiki.OpsSchemaShowTemplate(space, typeName)
					return text, false, err
				}
				text, err := wiki.OpsSchemaShow(space, typeName)
				return text, false, err
			case "add":
				typeName := a.str("type")
				if typeName == "" {
					return "", true, fmt.Errorf("type is required for add")
				}
				schemaPath := a.str("schema_path")
				if schemaPath == "" {
					return "", true, fmt.Errorf("schema_path is required for add")
				}
				text, err := wiki.OpsSchemaAdd(e, wikiName, typeName, schemaPath)
				return text, false, err
			case "remove":
				typeName := a.str("type")
				if typeName == "" {
					return "", true, fmt.Errorf("type is required for remove")
				}
				report, err := wiki.OpsSchemaRemove(e, wikiName, typeName, a.boolean("delete"), a.boolean("delete_pages"), a.boolean("dry_run"))
				if err != nil {
					return "", true, err
				}
				s.syncResources()
				return okJSON(report)
			case "validate":
				issues := wiki.OpsSchemaValidate(space)
				if a.str("type") != "" && !space.TypeRegistry.Has(a.str("type")) {
					issues = append(issues, fmt.Sprintf("type '%s' is not registered", a.str("type")))
				}
				if len(issues) == 0 {
					return "ok", false, nil
				}
				return strings.Join(issues, "\n"), false, nil
			default:
				return "", true, fmt.Errorf("unknown action: %s", action)
			}
		})

	s.addTool("wiki_info", "Return server version, config path, registered spaces, and index health",
		map[string]any{}, nil,
		func(ctx context.Context, ss *mcp.ServerSession, a args) (string, bool, error) {
			type infoPayload struct {
				Version     string   `json:"version"`
				ConfigPath  string   `json:"config_path"`
				Spaces      []string `json:"spaces"`
				DefaultWiki string   `json:"default_wiki"`
				IndexStatus string   `json:"index_status"`
			}
			info := infoPayload{
				Version:     Version,
				ConfigPath:  e.State.ConfigPath,
				DefaultWiki: e.State.Config.Global.DefaultWiki,
				IndexStatus: "ok",
			}
			for _, sp := range e.SpacesList() {
				info.Spaces = append(info.Spaces, sp.Name)
				if st := sp.IndexManager.Status(sp.RepoRoot); st.Stale || !st.Openable || !st.Queryable {
					info.IndexStatus = "degraded"
				}
			}
			return okJSON(info)
		})
}

func (s *Server) resolveWikiName(a args) (string, error) {
	name := a.str("wiki")
	if name == "" {
		name = s.Engine.DefaultWikiName()
	}
	if _, err := s.Engine.Space(name); err != nil {
		return "", err
	}
	return name, nil
}

func resolveUri(e *wiki.WikiEngine, uri, wikiFlag string) (*wiki.SpaceContext, wiki.Slug, error) {
	parsed, err := wiki.ParseWikiUri(uri)
	if err != nil {
		return nil, "", err
	}
	if parsed.Wiki != "" {
		if space, err := e.Space(parsed.Wiki); err == nil {
			return space, parsed.Slug, nil
		}
	}
	space, err := e.ResolveWiki(wikiFlag)
	if err != nil {
		return nil, "", err
	}
	return space, parsed.Slug, nil
}

// ── Resources ────────────────────────────────────────────────────────────────

// syncResources registers one resource per wiki page. The SDK emits
// notifications/resources/list_changed to subscribed sessions on changes.
func (s *Server) syncResources() {
	s.resMu.Lock()
	defer s.resMu.Unlock()
	type resource struct{ uri, name string }
	var wanted []resource
	for _, sp := range s.Engine.SpacesList() {
		ix := sp.IndexManager.Searcher()
		if ix == nil {
			continue
		}
		for _, d := range ix.Docs {
			wanted = append(wanted, resource{d.URI, d.Title})
		}
	}
	for _, w := range wanted {
		if s.registered[w.uri] {
			continue
		}
		uri := w.uri
		name := w.name
		s.MCP.AddResource(&mcp.Resource{URI: uri, Name: name, MIMEType: "text/markdown"},
			func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				content, err := s.readResource(uri)
				if err != nil {
					return nil, err
				}
				return &mcp.ReadResourceResult{
					Contents: []*mcp.ResourceContents{{URI: uri, Text: content}},
				}, nil
			})
		s.registered[uri] = true
	}
}

func (s *Server) readResource(uri string) (string, error) {
	result, err := wiki.ContentRead(s.Engine, uri, "", false, false)
	if err != nil {
		return "", err
	}
	if result.Kind != wiki.ContentPage {
		return "", fmt.Errorf("resource is not a text page: %s", uri)
	}
	return result.Content, nil
}

func strPtr(s string) *string { return &s }

// ── Transports ───────────────────────────────────────────────────────────────

// ServeStdio runs the MCP server over stdin/stdout until ctx is done.
func (s *Server) ServeStdio(ctx context.Context) error {
	return s.MCP.Run(ctx, &mcp.StdioTransport{})
}

// ServeHTTP runs a Streamable HTTP MCP server at /mcp with a Host header
// allowlist and bind retry. Binds 0.0.0.0:port.
func (s *Server) ServeHTTP(ctx context.Context, port int, allowedHosts []string, maxRestarts, backoffSecs int) error {
	allowed := map[string]bool{}
	for _, h := range allowedHosts {
		allowed[h] = true
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.MCP }, nil)
	mux := http.NewServeMux()
	mux.Handle("/mcp", hostAllowlist(handler, allowed))

	attempts := maxRestarts
	if attempts < 1 {
		attempts = 1
	}
	backoff := time.Duration(backoffSecs) * time.Second
	if backoff <= 0 {
		backoff = time.Second
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			if d := backoff * 2; d <= 30*time.Second {
				backoff = d
			}
		}
		server := &http.Server{Addr: fmt.Sprintf("0.0.0.0:%d", port), Handler: mux}
		errCh := make(chan error, 1)
		go func() { errCh <- server.ListenAndServe() }()
		select {
		case <-ctx.Done():
			return server.Shutdown(context.Background())
		case err := <-errCh:
			lastErr = err
		}
	}
	return fmt.Errorf("HTTP bind failed after %d attempts: %v", attempts, lastErr)
}

func hostAllowlist(next http.Handler, allowed map[string]bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(allowed) > 0 {
			host := r.Host
			if i := strings.LastIndexByte(host, ':'); i >= 0 {
				host = host[:i]
			}
			if !allowed[host] {
				http.Error(w, "host not allowed", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

var (
	_ = os.DirFS
	_ = filepath.Join
	_ = sort.Strings
)

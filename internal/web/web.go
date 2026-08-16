// Package web serves the read-only HTML UI: server-rendered pages over a
// mounted wiki engine. All data flows through the Ops* layer (AGENTS.md
// invariant #1); the package never mutates engine state.
package web

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"

	"llm-wiki-go/internal/wiki"
)

// Server renders the web UI for one engine and one default wiki.
type Server struct {
	engine      *wiki.WikiEngine
	defaultWiki string
	tpl         *template.Template
	md          goldmark.Markdown
}

// New returns the web UI handler. defaultWiki may be empty (the engine's
// default wiki is resolved per request).
func New(engine *wiki.WikiEngine, defaultWiki string) http.Handler {
	s := &Server{
		engine:      engine,
		defaultWiki: defaultWiki,
		tpl:         template.Must(template.New("web").Parse(tplSource)),
		// Default renderer config escapes raw HTML (no WithUnsafe):
		// <script> in page bodies renders as text, never executes.
		md: goldmark.New(
			goldmark.WithParserOptions(parser.WithAutoHeadingID()),
			goldmark.WithExtensions(extension.Table, extension.Strikethrough, extension.Linkify, extension.TaskList),
		),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleHome)
	mux.HandleFunc("/p/", s.handlePage)
	mux.HandleFunc("/search", s.handleSearch)
	mux.HandleFunc("/list", s.handleList)
	mux.HandleFunc("/list/", s.handleList)
	mux.HandleFunc("/graph", s.handleGraph)
	mux.HandleFunc("/graph.mmd", s.handleGraphDownload("mermaid"))
	mux.HandleFunc("/graph.dot", s.handleGraphDownload("dot"))
	mux.HandleFunc("/feed.xml", s.handleFeed)
	return mux
}

// ── data shapes ──────────────────────────────────────────────────────────────

type navType struct {
	Name, Label string
	Count       int
}

// baseData carries the layout fields every page template needs; page data
// structs embed it.
type baseData struct {
	Title    string
	Wiki     string
	Query    string
	NavTypes []navType
}

type homeData struct {
	baseData
	Stats    *wiki.WikiStats
	Recent   []wiki.RecentPage
	Activity []activityView
}

type activityView struct {
	Message string
	Date    string
}

type pageData struct {
	baseData
	Slug        string
	Title       string
	Type        string
	Status      string
	LastUpdated string
	// Confidence is the pre-formatted value ("0.90"); empty when the page
	// declares none — absence is semantic, never fabricated.
	Confidence string
	ConfDot    string
	Tags       []string
	Summary    string
	HTML       template.HTML
	Backlinks  []map[string]string
}

type searchView struct {
	Ref         wiki.PageRef
	ExcerptHTML template.HTML
}

type searchData struct {
	baseData
	Q        string
	Mode     string
	Fallback bool
	ModeNote bool
	Err      string
	Results  []searchView
}

type typeGroup struct {
	Name, Label string
	Pages       []wiki.PageSummary
}

type listData struct {
	baseData
	Type   string
	Label  string
	Total  int
	Groups []typeGroup
}

type graphData struct {
	baseData
	LLMS         string
	Nodes, Edges int
}

type msgData struct {
	baseData
	Message string
}

// ── helpers ──────────────────────────────────────────────────────────────────

func (s *Server) wikiName() string {
	if s.defaultWiki != "" {
		return s.defaultWiki
	}
	return s.engine.DefaultWikiName()
}

// base builds the layout data. stats may be nil (computed via OpsStats);
// "section" pages are structural and stay out of the type navigation.
func (s *Server) base(title, query string, stats *wiki.WikiStats) baseData {
	if stats == nil {
		st, err := wiki.OpsStats(s.engine, s.wikiName())
		if err != nil {
			st = &wiki.WikiStats{Types: map[string]int{}}
		}
		stats = st
	}
	names := make([]string, 0, len(stats.Types))
	for name := range stats.Types {
		if name != "section" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	nav := make([]navType, 0, len(names))
	for _, name := range names {
		nav = append(nav, navType{Name: name, Label: wiki.TitleCase(name), Count: stats.Types[name]})
	}
	return baseData{Title: title, Wiki: s.wikiName(), Query: query, NavTypes: nav}
}

func (s *Server) render(w http.ResponseWriter, name string, data any, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.tpl.ExecuteTemplate(w, name, data); err != nil {
		// Headers are already sent; nothing left but to truncate cleanly.
		fmt.Fprintf(os.Stderr, "web: template %s: %v\n", name, err)
	}
}

func (s *Server) notFound(w http.ResponseWriter, message string) {
	s.render(w, "msg", &msgData{baseData: s.base("404", "", nil), Message: message}, http.StatusNotFound)
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	s.render(w, "msg", &msgData{baseData: s.base("错误", "", nil), Message: err.Error()}, http.StatusInternalServerError)
}

// ── markdown rendering ───────────────────────────────────────────────────────

// renderMarkdown preprocesses wikilinks/relative destinations (fenced code
// excluded), then renders via goldmark. Source-dir rules mirror the index
// side (index.go IndexFile): the slug itself for bundle pages
// (<slug>/index.md), the parent directory for flat pages.
func (s *Server) renderMarkdown(body, slug string, isBundle bool) template.HTML {
	sourceDir := path.Dir(slug)
	if isBundle {
		sourceDir = slug
	}
	prepped := rewriteLinks(body, sourceDir)
	var buf bytes.Buffer
	if err := s.md.Convert([]byte(prepped), &buf); err != nil {
		return template.HTML(html.EscapeString(body))
	}
	return template.HTML(buf.String())
}

// rewriteLinks applies the §4 transformations line-wise, toggling between
// code and prose at ``` fences.
func rewriteLinks(body, sourceDir string) string {
	lines := strings.Split(body, "\n")
	inCode := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inCode = !inCode
			continue
		}
		if !inCode {
			lines[i] = rewriteWikilinks(rewriteDests(line, sourceDir))
		}
	}
	return strings.Join(lines, "\n")
}

// rewriteWikilinks turns [[slug]] and [[slug|label]] into CommonMark links.
// Invalid targets (cross-wiki URIs, malformed slugs) are left verbatim.
func rewriteWikilinks(line string) string {
	var out strings.Builder
	rest := line
	for {
		start := strings.Index(rest, "[[")
		if start < 0 {
			break
		}
		after := rest[start+2:]
		end := strings.Index(after, "]]")
		if end < 0 {
			break
		}
		raw := strings.TrimSpace(after[:end])
		if raw == "" {
			out.WriteString(rest[:start])
			out.WriteString("[[]]")
			rest = after[end+2:]
			continue
		}
		label, target := raw, raw
		if bar := strings.IndexByte(raw, '|'); bar >= 0 {
			label = strings.TrimSpace(raw[bar+1:])
			target = strings.TrimSpace(raw[:bar])
		}
		if pl := wiki.ParseParsedLink(target); !pl.CrossWiki {
			if _, err := wiki.NewSlug(pl.Slug); err == nil {
				if label == raw {
					label = wiki.Slug(pl.Slug).Title()
				}
				out.WriteString(rest[:start])
				fmt.Fprintf(&out, "[%s](/p/%s)", label, pl.Slug)
				rest = after[end+2:]
				continue
			}
		}
		// invalid target: emit verbatim and continue past this "]]"
		out.WriteString(rest[:start])
		out.WriteString(rest[start : start+2+end+2])
		rest = after[end+2:]
	}
	out.WriteString(rest)
	return out.String()
}

// rewriteDests normalizes relative CommonMark link destinations (./ ../)
// against sourceDir into /p/<slug> hrefs, mirroring the index-side
// extraction filters: images, absolute URLs, mailto, and anchors are left
// untouched.
func rewriteDests(line, sourceDir string) string {
	var out strings.Builder
	rest := line
	for {
		bracket := strings.Index(rest, "](")
		if bracket < 0 {
			break
		}
		before := rest[:bracket]
		afterParen := rest[bracket+2:]
		closing := strings.IndexByte(afterParen, ')')
		if closing < 0 {
			out.WriteString(rest[:bracket+2])
			rest = afterParen
			continue
		}
		dest := strings.TrimSpace(afterParen[:closing])
		open := strings.LastIndexByte(before, '[')
		isImage := open > 0 && before[open-1] == '!'
		rewritten := dest
		if !isImage {
			rewritten = hrefForDest(dest, sourceDir)
		}
		out.WriteString(before)
		out.WriteString("](")
		out.WriteString(rewritten)
		rest = afterParen[closing:]
	}
	out.WriteString(rest)
	return out.String()
}

func hrefForDest(dest, sourceDir string) string {
	anchor := ""
	if i := strings.IndexByte(dest, '#'); i >= 0 {
		anchor = dest[i:]
		dest = strings.TrimSpace(dest[:i])
	}
	if dest == "" || strings.HasPrefix(dest, "/") ||
		strings.HasPrefix(dest, "http://") || strings.HasPrefix(dest, "https://") ||
		strings.HasPrefix(dest, "mailto:") {
		return dest + anchor
	}
	if !strings.HasPrefix(dest, "./") && !strings.HasPrefix(dest, "../") && dest != ".." {
		return dest + anchor
	}
	if strings.ContainsAny(dest, " <>") {
		return dest + anchor
	}
	return "/p/" + wiki.NormalizeCommonmarkDest(dest, sourceDir) + anchor
}

// fmStr reads a string frontmatter value (the wiki package's accessor is
// unexported; ParsedPage covers only the common fields).
func fmStr(fm wiki.Frontmatter, key string) string {
	s, _ := fm[key].(string)
	return s
}

// ── handlers ─────────────────────────────────────────────────────────────────

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		s.notFound(w, "页面不存在："+r.URL.Path)
		return
	}
	name := s.wikiName()
	stats, err := wiki.OpsStats(s.engine, name)
	if err != nil {
		s.fail(w, err)
		return
	}
	recent, err := wiki.OpsRecentPages(s.engine, name, 10)
	if err != nil {
		s.fail(w, err)
		return
	}
	data := homeData{baseData: s.base(name, "", stats), Stats: stats, Recent: recent}
	if space, err := s.engine.Space(name); err == nil {
		// A non-git wiki simply shows no activity.
		if commits, err := wiki.GitRecentCommits(space.RepoRoot, 15); err == nil {
			for _, c := range commits {
				date := c.Date
				if len(date) >= 10 {
					date = date[:10]
				}
				data.Activity = append(data.Activity, activityView{Message: c.Message, Date: date})
			}
		}
	}
	s.render(w, "home", data, http.StatusOK)
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	slug, err := wiki.NewSlug(strings.TrimPrefix(r.URL.Path, "/p/"))
	if err != nil {
		s.notFound(w, "非法 slug")
		return
	}
	name := s.wikiName()
	content, err := wiki.ContentRead(s.engine, slug.String(), name, false, false)
	if err != nil || content.Kind != wiki.ContentPage {
		s.notFound(w, "页面不存在："+slug.String())
		return
	}
	page := wiki.ParseFrontmatter(content.Content)
	title := page.Title()
	if title == "" {
		title = slug.Title()
	}
	var conf string
	if v, ok := page.Frontmatter["confidence"].(float64); ok {
		conf = fmt.Sprintf("%.2f", v)
	}
	data := pageData{
		baseData:    s.base(title, "", nil),
		Slug:        slug.String(),
		Title:       title,
		Type:        page.PageType(),
		Status:      page.Status(),
		LastUpdated: fmStr(page.Frontmatter, "last_updated"),
		Confidence:  conf,
		ConfDot:     conf,
		Tags:        page.Tags(),
		Summary:     fmStr(page.Frontmatter, "summary"),
		Backlinks:   wiki.BacklinksQuery(s.engine, name, slug.String()),
	}
	isBundle := false
	if space, err := s.engine.Space(name); err == nil {
		if st, err := os.Stat(filepath.Join(space.WikiRoot, slug.String(), "index.md")); err == nil && st.Mode().IsRegular() {
			isBundle = true
		}
	}
	data.HTML = s.renderMarkdown(page.Body, slug.String(), isBundle)
	s.render(w, "page", data, http.StatusOK)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	mode := r.URL.Query().Get("mode")
	switch mode {
	case wiki.ModeSemantic, wiki.ModeHybrid:
	default:
		mode = wiki.ModeKeyword
	}
	name := s.wikiName()
	// Semantic modes need [embedding]; fall back to keyword with a notice
	// instead of erroring (plan §3).
	effective, fallback := mode, false
	if mode != wiki.ModeKeyword {
		if space, err := s.engine.Space(name); err == nil && space.Embed == nil {
			effective, fallback = wiki.ModeKeyword, true
		}
	}
	data := searchData{baseData: s.base("搜索", q, nil), Q: q, Mode: mode, Fallback: fallback}
	if q != "" {
		res, err := wiki.OpsSearch(s.engine, name, wiki.SearchParams{Query: q, Mode: effective, TopK: 25})
		if err != nil {
			data.Err = err.Error()
		} else {
			data.ModeNote = effective != wiki.ModeKeyword
			for _, ref := range res.Results {
				v := searchView{Ref: ref}
				if ref.Excerpt != nil {
					// Excerpts are pre-escaped HTML with <mark> highlights.
					v.ExcerptHTML = template.HTML(*ref.Excerpt)
				}
				data.Results = append(data.Results, v)
			}
		}
	}
	s.render(w, "search", data, http.StatusOK)
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	typeFilter := strings.Trim(r.URL.Path, "/")
	typeFilter = strings.TrimPrefix(typeFilter, "list")
	typeFilter = strings.Trim(typeFilter, "/")
	name := s.wikiName()
	list, _, err := wiki.OpsList(s.engine, name, typeFilter, "", 1, 500)
	if err != nil {
		s.fail(w, err)
		return
	}
	data := listData{baseData: s.base("列表", "", nil), Type: typeFilter, Total: list.Total}
	if typeFilter != "" {
		data.Label = wiki.TitleCase(typeFilter)
		data.Groups = []typeGroup{{Name: typeFilter, Label: data.Label, Pages: list.Pages}}
	} else {
		data.Label = "全部页面"
		groups := map[string][]wiki.PageSummary{}
		for _, p := range list.Pages {
			if p.Type == "section" {
				continue
			}
			key := p.Type
			if key == "" {
				key = "page"
			}
			groups[key] = append(groups[key], p)
		}
		keys := make([]string, 0, len(groups))
		for k := range groups {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			data.Groups = append(data.Groups, typeGroup{Name: k, Label: wiki.TitleCase(k), Pages: groups[k]})
		}
	}
	s.render(w, "list", data, http.StatusOK)
}

func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	res, err := wiki.OpsGraphBuild(s.engine, s.wikiName(), wiki.GraphParams{Format: "llms"})
	if err != nil {
		s.fail(w, err)
		return
	}
	data := graphData{
		baseData: s.base("概念图", "", nil),
		LLMS:     res.Rendered,
		Nodes:    res.Report.Nodes,
		Edges:    res.Report.Edges,
	}
	s.render(w, "graph", data, http.StatusOK)
}

func (s *Server) handleGraphDownload(format string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		res, err := wiki.OpsGraphBuild(s.engine, s.wikiName(), wiki.GraphParams{Format: format})
		if err != nil {
			s.fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, res.Rendered)
	}
}

func (s *Server) handleFeed(w http.ResponseWriter, r *http.Request) {
	name := s.wikiName()
	pages, err := wiki.OpsRecentPages(s.engine, name, 20)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	base := "http://" + r.Host
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString("<rss version=\"2.0\"><channel>")
	fmt.Fprintf(&b, "<title>%s</title>", xmlEsc(name))
	fmt.Fprintf(&b, "<link>%s</link>", xmlEsc(base+"/"))
	fmt.Fprintf(&b, "<description>llm-wiki: recently updated pages</description>")
	for _, p := range pages {
		b.WriteString("<item>")
		title := p.Title
		if title == "" {
			title = p.Slug
		}
		fmt.Fprintf(&b, "<title>%s</title>", xmlEsc(title))
		fmt.Fprintf(&b, "<link>%s</link>", xmlEsc(base+"/p/"+p.Slug))
		fmt.Fprintf(&b, "<guid>%s</guid>", xmlEsc("urn:llm-wiki:"+name+":"+p.Slug))
		if p.Summary != "" {
			fmt.Fprintf(&b, "<description>%s</description>", xmlEsc(p.Summary))
		}
		if t, err := time.Parse("2006-01-02", p.LastUpdated); err == nil {
			fmt.Fprintf(&b, "<pubDate>%s</pubDate>", t.UTC().Format(time.RFC1123Z))
		}
		b.WriteString("</item>")
	}
	b.WriteString("</channel></rss>")
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	fmt.Fprint(w, b.String())
}

func xmlEsc(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

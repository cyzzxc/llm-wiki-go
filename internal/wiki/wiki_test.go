package wiki

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"llm-wiki-go/internal/tokenizer"
)

// buildFixture creates a temp home with one registered wiki containing
// Chinese + English pages, ingested and indexed. Returns the engine and
// paths.
func buildFixture(t *testing.T) (*WikiEngine, string, string) {
	t.Helper()
	home := t.TempDir()
	repo := filepath.Join(home, "mywiki")
	configPath := filepath.Join(home, ".llm-wiki", "config.toml")

	if _, err := SpacesCreate(repo, "mywiki", "", false, true, configPath, ""); err != nil {
		t.Fatal(err)
	}
	pages := map[string]string{
		"wiki/concepts/attention.md": `---
title: "注意力机制"
type: concept
status: active
last_updated: "2026-08-16"
summary: "Transformer 的核心检索机制，通过查询键值对加权聚合信息"
confidence: 0.9
tags:
  - deep-learning
concepts:
  - concepts/moe
---
# 注意力机制

注意力机制（Attention）让模型在处理每个位置时动态关注最相关的信息。
它通过计算 [[concepts/moe]] 与查询的相似度分配权重。
参见 [RNN 基础](../concepts/rnn.md)。
`,
		"wiki/concepts/moe.md": `---
title: "混合专家模型"
type: concept
status: active
last_updated: "2026-08-15"
summary: "MoE：门控网络将输入路由到少量专家网络以扩大模型容量"
confidence: 0.8
tags:
  - deep-learning
  - transformer
concepts:
  - concepts/attention
---
# 混合专家模型

门控网络（Router）为每个 token 选择最合适的专家组合，保持计算效率的同时扩大容量。
相关：[[concepts/attention]]
`,
		"wiki/concepts/rnn.md": `---
title: "循环神经网络"
type: concept
status: draft
last_updated: "2026-07-01"
summary: "按序列逐步处理输入的经典网络结构"
tags:
  - deep-learning
---
# 循环神经网络

RNN 按时间步展开，适合早期序列建模任务。English keyword: recurrent gating.
`,
	}
	for rel, content := range pages {
		path := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := OpsIngest(mustEngine(t, configPath), "mywiki", ".", false, false); err != nil {
		t.Fatal(err)
	}
	return mustEngine(t, configPath), home, repo
}

func mustEngine(t *testing.T, configPath string) *WikiEngine {
	t.Helper()
	e, err := BuildEngine(configPath)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestChineseSearch(t *testing.T) {
	e, _, _ := buildFixture(t)

	cases := []struct {
		query       string
		wantTopSlug string
	}{
		{"注意力机制", "concepts/attention"},
		{"混合专家模型", "concepts/moe"},
		{"门控网络 路由", "concepts/moe"},
		{"recurrent gating", "concepts/rnn"},
	}
	for _, tc := range cases {
		result, err := OpsSearch(e, "mywiki", SearchParams{Query: tc.query})
		if err != nil {
			t.Fatalf("query %q: %v", tc.query, err)
		}
		if len(result.Results) == 0 {
			t.Fatalf("query %q: no results", tc.query)
		}
		if result.Results[0].Slug != tc.wantTopSlug {
			t.Fatalf("query %q: top = %s, want %s", tc.query, result.Results[0].Slug, tc.wantTopSlug)
		}
		if result.Results[0].Excerpt == nil || *result.Results[0].Excerpt == "" {
			t.Fatalf("query %q: missing excerpt", tc.query)
		}
	}
}

func TestSearchAcrossProcessReload(t *testing.T) {
	// The persisted index must survive a fresh engine (gob reload rebuilds
	// bySlug/df — regression test for the empty-search bug).
	e, _, _ := buildFixture(t)
	e2, err := BuildEngine(e.State.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := OpsSearch(e2, "mywiki", SearchParams{Query: "注意力"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) == 0 || result.Results[0].Slug != "concepts/attention" {
		t.Fatalf("reload lost index: %+v", result.Results)
	}
}

func TestListAndFacets(t *testing.T) {
	e, _, _ := buildFixture(t)
	list, _, err := OpsList(e, "mywiki", "", "", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 3 {
		t.Fatalf("total = %d, want 3", list.Total)
	}
	if list.Facets.Type["concept"] != 3 {
		t.Fatalf("type facet = %v", list.Facets.Type)
	}
	if list.Facets.Status["active"] != 2 || list.Facets.Status["draft"] != 1 {
		t.Fatalf("status facet = %v", list.Facets.Status)
	}
}

func TestGraphEdgesAndRender(t *testing.T) {
	e, _, _ := buildFixture(t)
	result, err := OpsGraphBuild(e, "mywiki", GraphParams{Format: "mermaid"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Nodes != 3 {
		t.Fatalf("nodes = %d, want 3", result.Report.Nodes)
	}
	if !strings.Contains(result.Rendered, "depends-on") {
		t.Fatalf("missing depends-on edge in:\n%s", result.Rendered)
	}
	llms, err := OpsGraphBuild(e, "mywiki", GraphParams{Format: "llms"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(llms.Rendered, "Key hubs") {
		t.Fatalf("llms render missing hubs")
	}
}

func TestStats(t *testing.T) {
	e, _, _ := buildFixture(t)
	stats, err := OpsStats(e, "mywiki")
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pages != 3 {
		t.Fatalf("pages = %d", stats.Pages)
	}
	if stats.Index.Stale {
		t.Fatalf("index unexpectedly stale")
	}
	if stats.Diameter == nil || *stats.Diameter < 1 {
		t.Fatalf("diameter = %v", stats.Diameter)
	}
}

func TestLintRules(t *testing.T) {
	e, _, _ := buildFixture(t)
	report, err := OpsLint(e, "mywiki", "", "")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, f := range report.Findings {
		seen[f.Rule]++
	}
	if seen["missing-fields"] == 0 {
		t.Fatalf("expected missing-fields findings (read_when required)")
	}
	if seen["articulation-point"] == 0 && seen["bridge"] == 0 && seen["periphery"] == 0 {
		t.Fatalf("expected structural findings on a path graph")
	}
}

func TestSuggest(t *testing.T) {
	e, _, _ := buildFixture(t)
	sug, err := OpsSuggest(e, "mywiki", "concepts/moe", 5)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range sug {
		if s.Slug == "concepts/rnn" || s.Slug == "concepts/attention" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected related suggestions, got %+v", sug)
	}
}

func TestExportFormats(t *testing.T) {
	e, home, _ := buildFixture(t)
	txt := filepath.Join(home, "llms.txt")
	report, err := OpsExport(e, "mywiki", txt, ExportLLMSTxt, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Pages != 3 {
		t.Fatalf("exported %d pages", report.Pages)
	}
	raw, _ := os.ReadFile(txt)
	if !strings.Contains(string(raw), "# mywiki") || !strings.Contains(string(raw), "混合专家模型") {
		t.Fatalf("llms.txt missing content:\n%s", raw)
	}
	js, err := OpsExport(e, "mywiki", filepath.Join(home, "llms.json"), ExportJSON, true)
	if err != nil || js.Pages != 3 {
		t.Fatalf("json export: %v %+v", err, js)
	}
}

func TestContentLifecycle(t *testing.T) {
	e, _, _ := buildFixture(t)
	// new
	created, err := ContentNew(e, "concepts/newpage", "mywiki", false, false, "新页面", "")
	if err != nil {
		t.Fatal(err)
	}
	if created.Slug != "concepts/newpage" {
		t.Fatalf("created slug = %s", created.Slug)
	}
	// write
	if _, _, err := ContentWrite(e, "concepts/newpage", "---\ntitle: 新\ntype: concept\n---\n正文", "mywiki"); err != nil {
		t.Fatal(err)
	}
	// read (no frontmatter)
	read, err := ContentRead(e, "concepts/newpage", "mywiki", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if read.Kind != ContentPage || strings.Contains(read.Content, "title:") {
		t.Fatalf("unexpected read: %q", read.Content)
	}
	// resolve
	res, err := ResolveUriToPath(e, "concepts/newpage", "mywiki")
	if err != nil || !res.Exists {
		t.Fatalf("resolve: %+v %v", res, err)
	}
	// commit
	hash, err := ContentCommit(e, "mywiki", []string{"concepts/newpage"}, false, "test: newpage")
	if err != nil {
		t.Fatal(err)
	}
	if hash == "" {
		t.Fatal("expected a commit hash")
	}
	// history
	hist, err := OpsHistory(e, "mywiki", "concepts/newpage", 5, nil)
	if err != nil || len(hist.Entries) == 0 {
		t.Fatalf("history: %+v %v", hist, err)
	}
}

func TestIngestDryRunAndRedact(t *testing.T) {
	e, home, _ := buildFixture(t)
	repo := filepath.Join(home, "mywiki")
	secret := filepath.Join(repo, "wiki", "secrets.md")
	os.WriteFile(secret, []byte("---\ntitle: secrets\ntype: note\n---\nkey: ghp_"+strings.Repeat("a", 36)+" end\n"), 0o644)

	report, _, err := OpsIngest(e, "mywiki", "secrets.md", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.PagesValidated != 1 {
		t.Fatalf("dry run validated %d pages", report.PagesValidated)
	}
	// file untouched in dry run
	raw, _ := os.ReadFile(secret)
	if !strings.Contains(string(raw), "ghp_") {
		t.Fatal("dry run must not modify files")
	}

	report2, _, err := OpsIngest(e, "mywiki", "secrets.md", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(report2.Redacted) == 0 {
		t.Fatal("expected redaction report")
	}
	raw2, _ := os.ReadFile(secret)
	if strings.Contains(string(raw2), "ghp_") {
		t.Fatal("secret survived redaction")
	}
}

func TestIncrementalIndexUpdate(t *testing.T) {
	e, home, _ := buildFixture(t)
	repo := filepath.Join(home, "mywiki")
	newPage := filepath.Join(repo, "wiki", "concepts", "newkid.md")
	os.WriteFile(newPage, []byte("---\ntitle: 新词条\ntype: concept\nstatus: active\n---\n全新增量内容：稀疏专家\n"), 0o644)

	if _, _, err := OpsIngest(e, "mywiki", ".", false, false); err != nil {
		t.Fatal(err)
	}
	e2 := mustEngine(t, e.State.ConfigPath)
	result, err := OpsSearch(e2, "mywiki", SearchParams{Query: "稀疏专家"})
	if err != nil {
		t.Fatal(err)
	}
	foundNew := false
	for _, r := range result.Results {
		if r.Slug == "concepts/newkid" {
			foundNew = true
		}
	}
	if !foundNew {
		t.Fatalf("incremental update lost new page: %+v", result.Results)
	}
	status, _, _ := OpsIndexStatus(e2, "mywiki")
	if status.Stale {
		t.Fatal("index stale after update")
	}
}

func TestTokenizerPersistence(t *testing.T) {
	// Index built with auto tokenizer must keep Chinese search working
	// after reload (tokenizer name recorded in the index).
	e, _, _ := buildFixture(t)
	space, _ := e.Space("mywiki")
	if space.Tokenizer.Name() != "auto" {
		t.Fatalf("tokenizer = %s", space.Tokenizer.Name())
	}
	e2 := mustEngine(t, e.State.ConfigPath)
	space2, _ := e2.Space("mywiki")
	ix := space2.IndexManager.Searcher()
	if ix == nil || ix.TokenizerName != "auto" {
		t.Fatalf("index tokenizer lost: %+v", ix)
	}
	_ = tokenizer.New
}

func TestBacklinks(t *testing.T) {
	e, _, _ := buildFixture(t)
	links := BacklinksQuery(e, "mywiki", "concepts/moe")
	if len(links) == 0 {
		t.Fatal("expected backlinks to concepts/moe")
	}
	found := false
	for _, l := range links {
		if l["slug"] == "concepts/attention" {
			found = true
		}
	}
	if !found {
		t.Fatalf("backlinks = %+v", links)
	}
}

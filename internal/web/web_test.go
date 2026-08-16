package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"llm-wiki-go/internal/wiki"
)

// buildFixture creates a wiki with Chinese pages, ingests it, commits it
// (home activity needs git history), and serves the web UI over it.
func buildFixture(t *testing.T) *httptest.Server {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "mywiki")
	configPath := filepath.Join(home, ".llm-wiki", "config.toml")
	if _, err := wiki.SpacesCreate(repo, "mywiki", "", false, true, configPath, ""); err != nil {
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
---
# 混合专家模型

门控网络（Router）为每个 token 选择最合适的专家组合。
相关：[[concepts/attention|注意力]]
`,
		"wiki/concepts/rnn.md": `---
title: "循环神经网络"
type: concept
status: draft
last_updated: "2026-07-01"
summary: "按序列逐步处理输入的经典网络结构"
---
# 循环神经网络

RNN 按时间步展开，适合早期序列建模任务。English keyword: recurrent gating.
`,
		// Bundle page: exercises the sourceDir=slug link rule, raw-HTML
		// escaping, and fenced-code immunity.
		"wiki/notes/transformer-notes/index.md": `---
title: "Transformer 读书笔记"
type: doc
status: active
last_updated: "2026-08-14"
summary: "阅读笔记与链接种子"
---
# Transformer 读书笔记

<linked from> 注意 [注意力](../../concepts/attention.md) 的缩放点积形式。

<script>alert('xss')</script>

` + "```" + `
[[concepts/rnn]] fenced block stays verbatim
` + "```" + `
`,
	}
	for rel, content := range pages {
		p := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Commit so the home page has git activity (repo-local identity — the
	// test host may have none).
	for _, kv := range [][2]string{{"user.name", "tester"}, {"user.email", "t@example.com"}} {
		cmd := exec.Command("git", "-C", repo, "config", kv[0], kv[1])
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git config %s: %s", kv[0], out)
		}
	}
	if _, err := wiki.GitCommit(repo, "init: seed 中文页面"); err != nil {
		t.Fatal(err)
	}
	seed, err := wiki.BuildEngine(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := wiki.OpsIngest(seed, "mywiki", ".", false, false); err != nil {
		t.Fatal(err)
	}
	engine, err := wiki.BuildEngine(configPath)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(New(engine, "mywiki"))
	t.Cleanup(ts.Close)
	return ts
}

func get(t *testing.T, ts *httptest.Server, path string) (int, string) {
	t.Helper()
	resp, err := ts.Client().Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

func TestHome(t *testing.T) {
	ts := buildFixture(t)
	status, body := get(t, ts, "/")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if !strings.Contains(body, `href="/list/concept"`) {
		t.Error("home: missing concept type pill")
	}
	if !strings.Contains(body, "init: seed 中文页面") {
		t.Error("home: missing activity entry")
	}
	if !strings.Contains(body, "注意力机制") {
		t.Error("home: missing recently-tended entry")
	}
}

func TestPageRender(t *testing.T) {
	ts := buildFixture(t)
	status, body := get(t, ts, "/p/concepts/attention")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if !strings.Contains(body, `href="/p/concepts/moe"`) {
		t.Error("page: wikilink not rewritten to /p/ href")
	}
	if !strings.Contains(body, `href="/p/concepts/rnn"`) {
		t.Error("page: relative .md link not normalized")
	}
	if !strings.Contains(body, "注意力机制") || !strings.Contains(body, "动态关注") {
		t.Error("page: Chinese body missing")
	}

	// Bundle page: ./ ../ against the slug itself; XSS escaping; code immunity.
	status, body = get(t, ts, "/p/notes/transformer-notes")
	if status != http.StatusOK {
		t.Fatalf("bundle page status = %d", status)
	}
	if !strings.Contains(body, `href="/p/concepts/attention"`) {
		t.Error("bundle page: ../../ link not normalized against slug sourceDir")
	}
	if strings.Contains(body, "<script>alert") {
		t.Error("bundle page: raw <script> leaked into HTML")
	}
	if strings.Contains(body, "alert('xss')") {
		t.Error("bundle page: script payload survived rendering")
	}
	if !strings.Contains(body, "<!-- raw HTML omitted -->") {
		t.Error("bundle page: raw HTML not neutralized by goldmark")
	}
	if !strings.Contains(body, "[[concepts/rnn]] fenced block stays verbatim") {
		t.Error("bundle page: fenced code wikilink was rewritten")
	}
	if strings.Count(body, "[[concepts/rnn]]") != 1 {
		t.Errorf("bundle page: expected verbatim [[concepts/rnn]] once (fenced), got %d", strings.Count(body, "[[concepts/rnn]]"))
	}
}

func TestInvalidSlug(t *testing.T) {
	ts := buildFixture(t)
	// The mux path-cleans ../ into a redirect first; the followed response
	// must still be a 404 (NewSlug rejects the traversal).
	status, _ := get(t, ts, "/p/../etc")
	if status != http.StatusNotFound {
		t.Fatalf("/p/../etc status = %d, want 404", status)
	}
	status, _ = get(t, ts, "/p/concepts/../../etc")
	if status != http.StatusNotFound {
		t.Fatalf("/p/concepts/../../etc status = %d, want 404", status)
	}
	status, _ = get(t, ts, "/p/nonexistent")
	if status != http.StatusNotFound {
		t.Fatalf("/p/nonexistent status = %d, want 404", status)
	}
}

func TestSearch(t *testing.T) {
	ts := buildFixture(t)
	status, body := get(t, ts, "/search?q="+url.QueryEscape("注意力"))
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if !strings.Contains(body, "注意力机制") {
		t.Error("search: target page missing from results")
	}
	if !strings.Contains(body, "concepts/attention") {
		t.Error("search: result link missing")
	}

	status, body = get(t, ts, "/search?q="+url.QueryEscape("<script>alert(1)</script>"))
	if status != http.StatusOK {
		t.Fatalf("xss query status = %d", status)
	}
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("search: raw <script> query echoed into HTML")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("search: query not escaped on echo")
	}

	// hybrid without [embedding] falls back to keyword with a notice.
	status, body = get(t, ts, "/search?q="+url.QueryEscape("注意力机制")+"&mode=hybrid")
	if status != http.StatusOK {
		t.Fatalf("hybrid status = %d", status)
	}
	if !strings.Contains(body, "回退关键词检索") {
		t.Error("search: hybrid fallback notice missing")
	}
	if !strings.Contains(body, "注意力机制") {
		t.Error("search: fallback produced no results")
	}
}

func TestList(t *testing.T) {
	ts := buildFixture(t)
	status, body := get(t, ts, "/list/concept")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if !strings.Contains(body, "注意力机制") || !strings.Contains(body, "循环神经网络") {
		t.Error("list: concept pages missing")
	}
	status, body = get(t, ts, "/list")
	if status != http.StatusOK {
		t.Fatalf("/list status = %d", status)
	}
	if !strings.Contains(body, "Concept") || !strings.Contains(body, "Doc") {
		t.Error("list: grouped type sections missing")
	}
}

func TestGraph(t *testing.T) {
	ts := buildFixture(t)
	status, body := get(t, ts, "/graph")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if !strings.Contains(body, "Key hubs") {
		t.Error("graph: llms render missing key hubs")
	}
	status, body = get(t, ts, "/graph.mmd")
	if status != http.StatusOK {
		t.Fatalf("/graph.mmd status = %d", status)
	}
	if !strings.HasPrefix(body, "graph LR\n") {
		t.Errorf("/graph.mmd starts with %q", body[:min(40, len(body))])
	}
	status, body = get(t, ts, "/graph.dot")
	if status != http.StatusOK || !strings.Contains(body, "digraph") {
		t.Errorf("/graph.dot status=%d contains digraph=%v", status, strings.Contains(body, "digraph"))
	}
}

func TestFeed(t *testing.T) {
	ts := buildFixture(t)
	status, body := get(t, ts, "/feed.xml")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if !strings.HasPrefix(body, "<?xml") {
		t.Error("feed: missing xml declaration")
	}
	if !strings.Contains(body, "<item>") {
		t.Error("feed: no items")
	}
	if !strings.Contains(body, "concepts/attention") {
		t.Error("feed: attention page missing")
	}
}

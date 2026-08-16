package wiki

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── S2/S3: git 重命名 + 中文路径增量更新 ─────────────────────────────────────

func TestRenameUpdatesIndex(t *testing.T) {
	e, home, _ := buildFixture(t)
	repo := filepath.Join(home, "mywiki")
	old := filepath.Join(repo, "wiki", "concepts", "moe.md")
	newPath := filepath.Join(repo, "wiki", "concepts", "混合专家.md")

	if err := os.Rename(old, newPath); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpsIngest(e, "mywiki", ".", false, false); err != nil {
		t.Fatal(err)
	}

	e2 := mustEngine(t, e.State.ConfigPath)
	space, _ := e2.Space("mywiki")
	ix := space.IndexManager.Searcher()
	if ix == nil {
		t.Fatal("no index")
	}
	if d := ix.Doc("concepts/moe"); d != nil {
		t.Fatalf("old slug survived rename: %+v", d)
	}
	if d := ix.Doc("concepts/混合专家"); d == nil {
		t.Fatal("new slug missing after rename (quotePath or rename parsing)")
	}
}

// ── S6(索引侧): 删除页从索引摘除 ─────────────────────────────────────────────

func TestDeleteRemovesFromIndex(t *testing.T) {
	e, home, _ := buildFixture(t)
	repo := filepath.Join(home, "mywiki")
	if err := os.Remove(filepath.Join(repo, "wiki", "concepts", "rnn.md")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpsIngest(e, "mywiki", ".", false, false); err != nil {
		t.Fatal(err)
	}
	e2 := mustEngine(t, e.State.ConfigPath)
	space, _ := e2.Space("mywiki")
	ix := space.IndexManager.Searcher()
	if ix == nil {
		t.Fatal("no index")
	}
	if d := ix.Doc("concepts/rnn"); d != nil {
		t.Fatalf("deleted slug still indexed: %+v", d)
	}
	result, err := OpsSearch(e2, "mywiki", SearchParams{Query: "recurrent gating"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range result.Results {
		if r.Slug == "concepts/rnn" {
			t.Fatal("deleted page still searchable")
		}
	}
}

// ── S4: 图缓存跨进程不吃旧提交的快照 ────────────────────────────────────────

func TestGraphCacheInvalidatedAcrossProcesses(t *testing.T) {
	e, home, _ := buildFixture(t)

	// 进程 A 构建图
	spaceA, _ := e.Space("mywiki")
	gA, err := spaceA.GetOrBuildGraph(GraphFilter{})
	if err != nil {
		t.Fatal(err)
	}
	nodesA := gA.NodeCount()

	// 变更：新增互链页 + ingest（LastCommit 变化）
	extra := filepath.Join(home, "mywiki", "wiki", "concepts", "extra.md")
	os.WriteFile(extra, []byte("---\ntitle: extra\ntype: concept\nstatus: active\n---\n[[concepts/attention]] [[concepts/moe]]\n"), 0o644)
	if _, _, err := OpsIngest(e, "mywiki", ".", false, false); err != nil {
		t.Fatal(err)
	}

	// 进程 B：新引擎挂载（WarmStart 路径），图必须反映新提交
	e2 := mustEngine(t, e.State.ConfigPath)
	spaceB, _ := e2.Space("mywiki")
	gB, err := spaceB.GetOrBuildGraph(GraphFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if gB.NodeCount() != nodesA+1 {
		t.Fatalf("graph across process served stale snapshot: %d nodes, want %d", gB.NodeCount(), nodesA+1)
	}
}

// ── S7: bundle 资产 URI 带 wiki 名 ──────────────────────────────────────────

func TestAssetURIContainsWikiName(t *testing.T) {
	e, home, _ := buildFixture(t)
	repo := filepath.Join(home, "mywiki")
	bundleDir := filepath.Join(repo, "wiki", "concepts", "moe")
	os.MkdirAll(bundleDir, 0o755)
	os.Rename(filepath.Join(repo, "wiki", "concepts", "moe.md"), filepath.Join(bundleDir, "index.md"))
	os.WriteFile(filepath.Join(bundleDir, "diagram.png"), []byte("png"), 0o644)

	result, err := ContentRead(e, "concepts/moe", "mywiki", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != ContentAssets || len(result.Assets) != 1 {
		t.Fatalf("assets = %+v", result.Assets)
	}
	want := "wiki://mywiki/concepts/moe/diagram.png"
	if result.Assets[0] != want {
		t.Fatalf("asset URI = %q, want %q", result.Assets[0], want)
	}
}

// ── S8: 自定义 wiki_root 时 git 提交落对仓库 ───────────────────────────────

func TestCustomWikiRootCommitsToRepo(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "nested")
	configPath := filepath.Join(home, ".llm-wiki", "config.toml")

	if _, err := SpacesCreate(repo, "nested", "", false, true, configPath, "content/notes"); err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(repo, "content", "notes", "page.md")
	os.MkdirAll(filepath.Dir(page), 0o755)
	os.WriteFile(page, []byte("---\ntitle: nested\ntype: concept\nstatus: active\n---\nbody\n"), 0o644)

	e, err := BuildEngine(configPath)
	if err != nil {
		t.Fatal(err)
	}
	report, _, err := OpsIngest(e, "nested", ".", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Commit == "" {
		t.Fatal("expected a commit")
	}
	// 提交必须在 repo 根可见（而不是 content/ 下的子仓库）
	if !dirExists(filepath.Join(repo, ".git")) {
		t.Fatal("repo git dir missing")
	}
	entries, err := os.ReadDir(filepath.Join(repo, "content"))
	if err != nil {
		t.Fatal(err)
	}
	for _, en := range entries {
		if en.Name() == ".git" {
			t.Fatal("git repo accidentally created inside content/")
		}
	}
	hist, err := GitPageHistory(repo, "content/notes/page.md", 5, false)
	if err != nil || len(hist) == 0 {
		t.Fatalf("page not committed at repo root: %+v %v", hist, err)
	}
}

// ── S1: schema add —— 未在 x-wiki-types 声明时写 wiki.toml ───────────────────

func TestSchemaAddWritesTomlWhenNotDeclared(t *testing.T) {
	e, home, _ := buildFixture(t)
	schemaPath := filepath.Join(home, "custom.json")
	os.WriteFile(schemaPath, []byte(`{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"required": ["title", "type"],
		"properties": {"title": {"type": "string"}, "type": {"type": "string"}}
	}`), 0o644)

	msg, err := OpsSchemaAdd(e, "mywiki", "custom-thing", schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "added [types.custom-thing] to wiki.toml") {
		t.Fatalf("expected wiki.toml registration message, got %q", msg)
	}
	wikiCfg, err := LoadWiki(filepath.Join(home, "mywiki"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := wikiCfg.Types["custom-thing"]; !ok {
		t.Fatal("[types.custom-thing] missing from wiki.toml")
	}

	// 已声明类型则不需要 toml 注册
	declared := filepath.Join(home, "declared.json")
	os.WriteFile(declared, []byte(`{
		"type": "object",
		"required": ["title"],
		"properties": {"title": {"type": "string"}},
		"x-wiki-types": {"thing2": "declared type"}
	}`), 0o644)
	msg2, err := OpsSchemaAdd(e, "mywiki", "thing2", declared)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(msg2, "wiki.toml") {
		t.Fatalf("declared type should not need toml, got %q", msg2)
	}
}

// ── B3: WritePage 拒绝非法 slug（防逃逸） ───────────────────────────────────

func TestWritePageRejectsInvalidSlug(t *testing.T) {
	e, home, _ := buildFixture(t)
	for _, bad := range []string{"../escape", "a/../../escape", "/abs", ".hidden", "file.md"} {
		if _, _, err := ContentWrite(e, bad, "x", "mywiki"); err == nil {
			t.Fatalf("slug %q must be rejected", bad)
		}
	}
	if fileExists(filepath.Join(home, "escape.md")) {
		t.Fatal("escape file was written outside wiki root")
	}
	_ = home
}

// ── B8: 提交返回完整 hash ──────────────────────────────────────────────────

func TestCommitReturnsFullHash(t *testing.T) {
	e, home, _ := buildFixture(t)
	// 先制造一个变更（fixture 已全部提交，--all 干净树会正确返回空串）
	page := filepath.Join(home, "mywiki", "wiki", "concepts", "moe.md")
	if err := os.WriteFile(page, []byte("---\ntitle: 混合专家模型\ntype: concept\nstatus: active\n---\n新增一行\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hash, err := ContentCommit(e, "mywiki", []string{"concepts/moe"}, false, "test: full hash")
	if err != nil {
		t.Fatal(err)
	}
	if len(hash) != 40 { // SHA-1 full hash; SHA-256 repos would be 64
		if len(hash) != 64 {
			t.Fatalf("commit returned %q (%d chars) — not a full hash", hash, len(hash))
		}
	}
}

// ── B5: api_key 打码 ───────────────────────────────────────────────────────

func TestAPIKeyMasked(t *testing.T) {
	e, _, _ := buildFixture(t)
	global, _ := LoadGlobal(e.State.ConfigPath)
	global.Embedding.APIKey = "ah-e5125069f335614470ef785e171b6b3b471b489dc588bcf8a46cb199cc020e34"
	global.Embedding.Enabled = true
	global.Embedding.BaseURL = "http://x"
	SaveGlobal(global, e.State.ConfigPath)
	e2 := mustEngine(t, e.State.ConfigPath)

	got, err := OpsConfigGet(e2, "mywiki", "embedding.api_key")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "e5125069f335614470ef785e171b6b3b471b489dc588bcf8a46cb199cc020e34") {
		t.Fatalf("api_key leaked: %q", got)
	}
	if !strings.Contains(got, "…") {
		t.Fatalf("api_key not masked: %q", got)
	}

	listing, err := OpsConfigListGlobal(e2)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(listing, "e5125069f335614470ef785e171b6b3b471b489dc588bcf8a46cb199cc020e34") {
		t.Fatal("config list leaked api_key")
	}

}

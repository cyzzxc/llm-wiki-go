package wiki

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	embedpkg "llm-wiki-go/internal/embed"
)

// semanticFixture builds a wiki with the embedding config pointed at a
// deterministic fake gateway (see internal/embed/embed_test.go's marker
// scheme: 专家→e0, 注意→e1, 循环/rnn→e2, 其它→e3).
func semanticFixture(t *testing.T) (*WikiEngine, *httptest.Server) {
	t.Helper()
	home := t.TempDir()
	repo := filepath.Join(home, "mywiki")
	configPath := filepath.Join(home, ".llm-wiki", "config.toml")

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		type item struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}
		var data []item
		for i, text := range req.Input {
			data = append(data, item{markerVector(text), i})
		}
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "model": req.Model, "data": data})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	if _, err := SpacesCreate(repo, "mywiki", "", false, true, configPath, ""); err != nil {
		t.Fatal(err)
	}
	pages := map[string]string{
		"wiki/concepts/moe.md":  "---\ntitle: 混合专家模型\ntype: concept\nstatus: active\nsummary: 门控路由到专家网络\n---\nMoE body\n",
		"wiki/concepts/attn.md": "---\ntitle: 注意力机制\ntype: concept\nstatus: active\nsummary: 查询键值加权\n---\nAttention body\n",
		"wiki/concepts/rnn.md":  "---\ntitle: 循环网络\ntype: concept\nstatus: active\nsummary: 按时间步展开\n---\nRNN body\n",
	}
	for rel, content := range pages {
		p := filepath.Join(repo, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), 0o644)
	}

	global, err := LoadGlobal(configPath)
	if err != nil {
		t.Fatal(err)
	}
	global.Embedding = embedpkg.Config{
		Enabled: true, BaseURL: srv.URL + "/v1", APIKey: "test",
		Model: "fake-embed", BatchSize: 8,
	}
	if err := SaveGlobal(global, configPath); err != nil {
		t.Fatal(err)
	}

	e, err := BuildEngine(configPath)
	if err != nil {
		t.Fatal(err)
	}
	// engine mounts lazily-indexed pages; ingest commits + embeds
	if _, _, err := OpsIngest(e, "mywiki", ".", false, false); err != nil {
		t.Fatal(err)
	}
	// re-mount so the persisted vectors are visible to a "fresh" engine
	e2, err := BuildEngine(configPath)
	if err != nil {
		t.Fatal(err)
	}
	return e2, srv
}

func markerVector(text string) []float32 {
	v := make([]float32, 4)
	switch {
	case strings.Contains(text, "专家"), strings.Contains(text, "expert"):
		v[0] = 1
	case strings.Contains(text, "注意"):
		v[1] = 1
	case strings.Contains(text, "循环"), strings.Contains(strings.ToLower(text), "rnn"):
		v[2] = 1
	default:
		v[3] = 1
	}
	return embedpkg.Normalize(v)
}

func TestSemanticSearchRanking(t *testing.T) {
	e, _ := semanticFixture(t)
	// 语义命中：查询词与目标页无词面重叠（「稀疏激活扩容」不在任何页面里），
	// 但 fake 网关把含「专家」的查询映射到 moe 的向量方向。
	result, err := OpsSearch(e, "mywiki", SearchParams{
		Query: "稀疏专家扩容方案", // contains 专家 → e0 → moe
		Mode:  ModeSemantic,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) == 0 || result.Results[0].Slug != "concepts/moe" {
		t.Fatalf("semantic top = %+v", result.Results)
	}
}

func TestHybridSearchBlend(t *testing.T) {
	e, _ := semanticFixture(t)
	// 「注意力」词面命中 attn（BM25），语义也指 attn；「专家」语义指 moe。
	// 混合模式应让两者都进结果。
	result, err := OpsSearch(e, "mywiki", SearchParams{
		Query: "注意力与专家路由",
		Mode:  ModeHybrid,
		TopK:  5,
	})
	if err != nil {
		t.Fatal(err)
	}
	slugs := map[string]bool{}
	for _, r := range result.Results {
		slugs[r.Slug] = true
	}
	if !slugs["concepts/attn"] || !slugs["concepts/moe"] {
		t.Fatalf("hybrid should surface both: %+v", result.Results)
	}
}

func TestSemanticRequiresConfig(t *testing.T) {
	e, _, _ := buildFixture(t) // fixture has no [embedding] section
	_, err := OpsSearch(e, "mywiki", SearchParams{Query: "x", Mode: ModeSemantic})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("expected not-configured error, got %v", err)
	}
}

func TestEmbeddingModelStateAndStaleness(t *testing.T) {
	e, _ := semanticFixture(t)
	status, _, err := OpsIndexStatus(e, "mywiki")
	if err != nil {
		t.Fatal(err)
	}
	if status.EmbeddingModel != "fake-embed" {
		t.Fatalf("state embedding model = %q", status.EmbeddingModel)
	}
	if status.EmbeddingDims != 4 {
		t.Fatalf("dims = %d", status.EmbeddingDims)
	}
	if status.Stale {
		t.Fatal("freshly built index should not be stale")
	}

	// 换模型 = 向量空间变化 → 全量重建
	global, _ := LoadGlobal(e.State.ConfigPath)
	global.Embedding.Model = "other-model"
	SaveGlobal(global, e.State.ConfigPath)
	e2, err := BuildEngine(e.State.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	status2, _, _ := OpsIndexStatus(e2, "mywiki")
	if !status2.Stale {
		t.Fatal("model change must mark the index stale")
	}
}

func TestQueryEmbedUsesGateway(t *testing.T) {
	e, srv := semanticFixture(t)
	space, _ := e.Space("mywiki")
	if space.Embed == nil {
		t.Fatal("embed client not mounted")
	}
	vecs, err := space.Embed.Embed(context.Background(), []string{"注意力查询"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 1 || len(vecs[0]) != 4 {
		t.Fatalf("query vec = %+v", vecs)
	}
	_ = srv
}

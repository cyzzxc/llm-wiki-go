package embed

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// fakeGateway is an OpenAI-compatible /embeddings endpoint returning
// deterministic vectors keyed by text markers.
type fakeGateway struct {
	t        *testing.T
	requests atomic.Int64
	failOnce atomic.Bool
}

func vecOf(text string) []float32 {
	switch {
	case contains(text, "专家"), contains(text, "expert"):
		return []float32{1, 0, 0, 0}
	case contains(text, "注意"):
		return []float32{0, 1, 0, 0}
	case contains(text, "循环"), contains(text, "rnn", "RNN"):
		return []float32{0, 0, 1, 0}
	default:
		return []float32{0, 0, 0, 1}
	}
}

func contains(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(s) >= len(sub) && indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func (f *fakeGateway) handler(w http.ResponseWriter, r *http.Request) {
	f.requests.Add(1)
	if f.failOnce.CompareAndSwap(true, false) {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var req embeddingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(400)
		return
	}
	if r.Header.Get("Authorization") == "" {
		w.WriteHeader(401)
		return
	}
	resp := embeddingsResponse{}
	for i, input := range req.Input {
		vec := Normalize(vecOf(input))
		resp.Data = append(resp.Data, struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}{vec, i})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func newTestClient(t *testing.T, gw *fakeGateway, batchSize int) *Client {
	srv := httptest.NewServer(http.HandlerFunc(gw.handler))
	t.Cleanup(srv.Close)
	return New(Config{
		Enabled: true, BaseURL: srv.URL, APIKey: "test-key",
		Model: "fake-embed", BatchSize: batchSize,
	})
}

func TestEmbedBatches(t *testing.T) {
	gw := &fakeGateway{t: t}
	c := newTestClient(t, gw, 2) // 5 inputs → 3 requests
	vecs, err := c.Embed(context.Background(), []string{
		"混合专家模型", "注意力机制", "循环网络", "专家门控", "无关文本",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := gw.requests.Load(); got != 3 {
		t.Fatalf("requests = %d, want 3", got)
	}
	if len(vecs) != 5 {
		t.Fatalf("vectors = %d", len(vecs))
	}
	// normalized: |v| == 1
	for i, v := range vecs {
		var sum float64
		for _, x := range v {
			sum += float64(x) * float64(x)
		}
		if math.Abs(sum-1) > 1e-5 {
			t.Fatalf("vec %d not unit length: %f", i, sum)
		}
	}
	// same marker → identical vector (determinism)
	if Cosine(vecs[0], vecs[3]) < 0.999 {
		t.Fatalf("专家 texts should be identical vectors: %f", Cosine(vecs[0], vecs[3]))
	}
}

func TestEmbedRetriesTransient(t *testing.T) {
	gw := &fakeGateway{t: t}
	gw.failOnce.Store(true) // first request 500 → retry
	c := newTestClient(t, gw, 16)
	if _, err := c.Embed(context.Background(), []string{"注意力"}); err != nil {
		t.Fatal(err)
	}
	if got := gw.requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2 (1 fail + 1 retry)", got)
	}
}

func TestCosineOrthogonal(t *testing.T) {
	a := Normalize([]float32{1, 0, 0})
	b := Normalize([]float32{0, 1, 0})
	if c := Cosine(a, b); math.Abs(c) > 1e-6 {
		t.Fatalf("orthogonal cosine = %f", c)
	}
	if c := Cosine(a, a); math.Abs(c-1) > 1e-6 {
		t.Fatalf("identical cosine = %f", c)
	}
}

func TestEmbedTextTruncation(t *testing.T) {
	c := New(Config{Enabled: true, BaseURL: "http://x", Model: "m", MaxTextChars: 10})
	got := c.EmbedText("标题", "摘要", string(make([]byte, 0))+repeatC("正", 100))
	if n := len([]rune(got)); n != 10 {
		t.Fatalf("truncated to %d runes, want 10", n)
	}
	if !contains(got, "标题") {
		t.Fatal("title should survive truncation")
	}
}

func repeatC(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

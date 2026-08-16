// Package embed is a minimal OpenAI-compatible embeddings client
// (POST {base_url}/embeddings) for semantic search. The gateway is an
// external service: everything here must be optional — with embedding
// disabled the engine behaves exactly as before (offline BM25 only).
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// Config is the [embedding] section. Global-only in the config file.
type Config struct {
	Enabled      bool    `toml:"enabled"`
	BaseURL      string  `toml:"base_url"`
	APIKey       string  `toml:"api_key"`
	Model        string  `toml:"model"`
	BatchSize    int     `toml:"batch_size"`
	MaxTextChars int     `toml:"max_text_chars"`
	TimeoutSecs  int     `toml:"timeout_secs"`
	HybridWeight float64 `toml:"hybrid_weight"`
}

// Defaults returns the built-in defaults (qwen3-embedding-8b via an
// OpenAI-compatible gateway, 4096 dims).
func Defaults() Config {
	return Config{
		Model:        "qwen3-embedding-8b",
		BatchSize:    16,
		MaxTextChars: 4000,
		TimeoutSecs:  60,
		HybridWeight: 0.5,
	}
}

// ApplyDefaults fills zero fields with defaults.
func (c Config) ApplyDefaults() Config {
	d := Defaults()
	if c.Model == "" {
		c.Model = d.Model
	}
	if c.BatchSize <= 0 {
		c.BatchSize = d.BatchSize
	}
	if c.MaxTextChars <= 0 {
		c.MaxTextChars = d.MaxTextChars
	}
	if c.TimeoutSecs <= 0 {
		c.TimeoutSecs = d.TimeoutSecs
	}
	if c.HybridWeight <= 0 {
		c.HybridWeight = d.HybridWeight
	}
	return c
}

// Usable reports whether the config is complete enough to build a client.
func (c Config) Usable() bool {
	return c.Enabled && c.BaseURL != ""
}

// Client calls the embeddings endpoint. Safe for concurrent use.
type Client struct {
	cfg Config
	hc  *http.Client
}

// New builds a client. apiKey falls back to LLM_WIKI_EMBEDDING_API_KEY.
func New(cfg Config) *Client {
	cfg = cfg.ApplyDefaults()
	key := cfg.APIKey
	if env := envAPIKey(); env != "" {
		key = env
	}
	cfg.APIKey = key
	return &Client{
		cfg: cfg,
		hc:  &http.Client{Timeout: time.Duration(cfg.TimeoutSecs) * time.Second},
	}
}

// Model returns the configured model name (stored alongside vectors —
// switching models invalidates the whole vector space).
func (c *Client) Model() string { return c.cfg.Model }

// Config returns the effective client config.
func (c *Client) Config() Config { return c.cfg }

// EmbedText is the deterministic page/query text construction: title,
// summary, then body, truncated to MaxTextChars runes.
func (c *Client) EmbedText(title, summary, body string) string {
	var b strings.Builder
	b.WriteString(title)
	if summary != "" {
		b.WriteString("\n")
		b.WriteString(summary)
	}
	if body != "" {
		b.WriteString("\n")
		b.WriteString(body)
	}
	s := b.String()
	if r := []rune(s); len(r) > c.cfg.MaxTextChars {
		return string(r[:c.cfg.MaxTextChars])
	}
	return s
}

type embeddingsRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingsResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Embed sends texts (batched internally) and returns unit-normalized
// vectors in input order. Retries transient failures twice.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	batch := c.cfg.BatchSize
	out := make([][]float32, len(texts))
	for start := 0; start < len(texts); start += batch {
		end := start + batch
		if end > len(texts) {
			end = len(texts)
		}
		vecs, err := c.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, fmt.Errorf("embed batch [%d:%d]: %w", start, end, err)
		}
		for i, v := range vecs {
			out[start+i] = Normalize(v)
		}
	}
	return out, nil
}

func (c *Client) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	payload, err := json.Marshal(embeddingsRequest{Model: c.cfg.Model, Input: texts})
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/embeddings"

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if c.cfg.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			lastErr = err
			continue // network error → retry
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("gateway %s: %s", resp.Status, truncate(string(body), 200))
			continue // transient → retry
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("gateway %s: %s", resp.Status, truncate(string(body), 200))
		}
		var decoded embeddingsResponse
		if err := json.Unmarshal(body, &decoded); err != nil {
			return nil, fmt.Errorf("invalid embeddings response: %v", err)
		}
		if decoded.Error != nil {
			return nil, fmt.Errorf("gateway error: %s", decoded.Error.Message)
		}
		if len(decoded.Data) != len(texts) {
			return nil, fmt.Errorf("gateway returned %d vectors for %d inputs", len(decoded.Data), len(texts))
		}
		sort.Slice(decoded.Data, func(i, j int) bool { return decoded.Data[i].Index < decoded.Data[j].Index })
		out := make([][]float32, len(texts))
		for i, d := range decoded.Data {
			out[i] = d.Embedding
		}
		return out, nil
	}
	return nil, lastErr
}

// Normalize scales v to unit length (cosine then equals the dot product).
func Normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	norm := math.Sqrt(sum)
	if norm == 0 {
		return v
	}
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) / norm)
	}
	return out
}

// Cosine returns the cosine similarity of two vectors (unit vectors make
// this a dot product).
func Cosine(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func envAPIKey() string { return os.Getenv("LLM_WIKI_EMBEDDING_API_KEY") }

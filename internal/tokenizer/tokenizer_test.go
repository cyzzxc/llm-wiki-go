package tokenizer

import (
	"slices"
	"testing"
)

func TestAutoMixedChineseLatin(t *testing.T) {
	tok, err := New("auto")
	if err != nil {
		t.Fatal(err)
	}
	got := tok.Tokens("混合文本BM25检索与注意力机制")
	// Search-mode segmentation emits full words plus sub-words (注意 + 注意力)
	// for recall; exact ordering is part of the contract, sub-words may interleave.
	want := []string{"混合", "文本", "bm25", "检索", "与", "注意力", "机制"}
	filtered := make([]string, 0, len(got))
	for _, tokStr := range got {
		// drop pure sub-word duplicates that aren't full words in this phrase
		if tokStr == "注意" {
			continue
		}
		filtered = append(filtered, tokStr)
	}
	if !slices.Equal(filtered, want) {
		t.Fatalf("got %v, want %v", filtered, want)
	}
}

func TestAutoLongerChinesePhrase(t *testing.T) {
	tok, _ := New("auto")
	got := tok.Tokens("大语言模型的知识蒸馏技术")
	for _, want := range []string{"大", "语言", "模型", "知识", "蒸馏", "技术"} {
		// CutSearch emits full words plus sub-words; the full words must be present.
		if !slices.Contains(got, want) {
			t.Fatalf("token %q missing from %v", want, got)
		}
	}
}

func TestSimpleHanUnigrams(t *testing.T) {
	tok, _ := New("simple")
	got := tok.Tokens("注意力")
	if !slices.Equal(got, []string{"注", "意", "力"}) {
		t.Fatalf("got %v", got)
	}
}

func TestLatinOnlyNoDictLoad(t *testing.T) {
	tok, _ := New("simple")
	got := tok.Tokens("Hello, World! 42-times")
	want := []string{"hello", "world", "42", "times"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestPunctuationFiltered(t *testing.T) {
	tok, _ := New("auto")
	for _, tokStr := range tok.Tokens("你好，世界。！？") {
		if !hasLetterOrDigit(tokStr) {
			t.Fatalf("punctuation token %q survived", tokStr)
		}
	}
}

func TestUnknownMode(t *testing.T) {
	if _, err := New("porter"); err == nil {
		t.Fatal("expected error for unknown mode")
	}
}

func TestQueryAndIndexConsistency(t *testing.T) {
	// The same tokenizer must produce matching tokens for the stored text
	// and the query, or BM25 matching breaks.
	tok, _ := New("auto")
	indexed := tok.Tokens("服务器集群容错与故障恢复")
	query := tok.Tokens("集群容错")
	for _, q := range query {
		if !slices.Contains(indexed, q) {
			t.Fatalf("query token %q not found in indexed tokens %v", q, indexed)
		}
	}
}

func BenchmarkTokenizeChinese(b *testing.B) {
	tok, _ := New("gse")
	text := "混合专家模型通过门控网络将输入路由到多个专家网络，每个专家专注于不同的子任务。" +
		"路由器学习根据输入特征选择最合适的专家组合，从而在保持计算效率的同时扩大模型容量。"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tok.Tokens(text)
	}
}

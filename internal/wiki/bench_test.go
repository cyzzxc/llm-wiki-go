package wiki

import (
	"fmt"
	"strings"
	"testing"

	"llm-wiki-go/internal/tokenizer"
)

func buildBenchIndex(b *testing.B, n int) *SearchIndex {
	tok, _ := tokenizer.New("gse")
	var docs []*IndexDoc
	topics := []string{"注意力机制", "混合专家模型", "知识蒸馏", "对比学习", "强化学习", "扩散模型", "检索增强生成", "思维链"}
	for i := 0; i < n; i++ {
		topic := topics[i%len(topics)]
		page := &ParsedPage{
			Frontmatter: Frontmatter{
				"title": fmt.Sprintf("%s #%d", topic, i),
				"type":  "concept", "status": "active",
				"summary": topic + "的方法总结与实践案例",
				"tags":    []any{"bench"},
			},
			Body: strings.Repeat(topic+"的研究进展与工程实践。核心方法涵盖训练技巧与落地分析。", 6),
		}
		slug, _ := NewSlug(fmt.Sprintf("concepts/page%04d", i))
		docs = append(docs, BuildIndexDoc(slug, "bench", page, benchSchema(), tok, "concepts"))
	}
	return NewSearchIndex(docs, tok.Name())
}

func benchSchema() *IndexSchema {
	return &IndexSchema{
		Fields: map[string]FieldKind{
			"slug": FieldKeyword, "uri": FieldKeyword, "body": FieldText, "body_links": FieldKeyword,
			"sources": FieldKeyword, "concepts": FieldKeyword, "last_updated": FieldText, "tags": FieldKeyword,
		},
	}
}

func BenchmarkSearchChinese1000(b *testing.B) {
	tok, _ := tokenizer.New("gse")
	ix := buildBenchIndex(b, 1000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Search("注意力机制 工程实践", SearchOptions{TopK: 10}, ix, tok); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSearchChinese10000(b *testing.B) {
	tok, _ := tokenizer.New("gse")
	ix := buildBenchIndex(b, 10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Search("混合专家模型 训练技巧", SearchOptions{TopK: 10}, ix, tok); err != nil {
			b.Fatal(err)
		}
	}
}

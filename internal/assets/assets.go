// Package assets embeds the Chinese segmentation dictionary and the
// default type schemas / body templates, keeping the binary self-contained.
package assets

import (
	"embed"
	"sort"
)

//go:embed dict/zh_s.txt
var dictFS embed.FS

// DictFile is the embedded gse-format Chinese dictionary (jieba word
// frequencies, ~350k entries, from go-ego/gse, Apache-2.0).
const DictFile = "dict/zh_s.txt"

// Dict returns the raw dictionary bytes.
func Dict() ([]byte, error) { return dictFS.ReadFile(DictFile) }

//go:embed schemas/*
var schemaFS embed.FS

// Schema returns an embedded default schema or body template by filename
// (e.g. "base.json", "concept.md"). Returns nil when absent.
func Schema(name string) []byte {
	b, err := schemaFS.ReadFile("schemas/" + name)
	if err != nil {
		return nil
	}
	return b
}

// SchemaNames lists embedded schema/template filenames, sorted.
func SchemaNames() []string {
	entries, err := schemaFS.ReadDir("schemas")
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

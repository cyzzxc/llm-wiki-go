// Package tokenizer provides text tokenization for the BM25 index,
// with first-class support for mixed Chinese/Latin content.
//
// Modes:
//
//	auto (default) — Han runs via gse dictionary segmentation (lazy-loaded
//	                 from the embedded dictionary on first use), everything
//	                 else via the simple word tokenizer.
//	gse / zh / cjk — same as auto, but the dictionary is loaded eagerly.
//	simple / en / en_stem (config compat) — no dictionary: Han runes become
//	                 single-character tokens, Latin runs become words.
package tokenizer

import (
	"strings"
	"sync"
	"unicode"

	"github.com/go-ego/gse"

	"llm-wiki-go/internal/assets"
)

// Tokenizer converts text into lowercase search tokens, duplicates preserved
// (BM25 needs term frequencies). It is safe for concurrent use.
type Tokenizer struct {
	name string
}

var (
	gseOnce sync.Once
	gseSeg  *gse.Segmenter
	gseErr  error
)

// loadGse lazily builds the dictionary segmenter. Loading the embedded
// dictionary takes a few hundred ms and ~100 MB RSS, so auto mode defers
// it until Han text is actually seen.
func loadGse() (*gse.Segmenter, error) {
	gseOnce.Do(func() {
		data, err := assets.Dict()
		if err != nil {
			gseErr = err
			return
		}
		var seg gse.Segmenter
		seg.AlphaNum = true
		if err := seg.LoadDictStr(string(data)); err != nil {
			gseErr = err
			return
		}
		gseSeg = &seg
	})
	return gseSeg, gseErr
}

// New returns a Tokenizer for the configured mode. Unknown mode names error.
func New(mode string) (*Tokenizer, error) {
	switch mode {
	case "auto", "cjk", "zh", "gse", "jieba":
		if mode != "auto" { // eager load — surface dict problems early
			if _, err := loadGse(); err != nil {
				return nil, err
			}
		}
	case "simple", "en", "en_stem":
	default:
		return nil, unknownModeError(mode)
	}
	return &Tokenizer{name: mode}, nil
}

type unknownModeError string

func (e unknownModeError) Error() string {
	return "unknown tokenizer mode: " + string(e) + " (expected auto, gse, or simple)"
}

// Name returns the configured mode name.
func (t *Tokenizer) Name() string { return t.name }

// Tokens tokenizes text for indexing or querying. Han script runs are
// segmented with the gse dictionary in search mode (full words plus
// sub-words, the standard CJK indexing strategy); other runs yield
// lowercased word tokens.
func (t *Tokenizer) Tokens(text string) []string {
	var out []string
	for _, run := range splitRuns(text) {
		if run.han {
			out = append(out, t.tokenizeHan(run.text)...)
		} else {
			out = append(out, latinWords(run.text)...)
		}
	}
	return out
}

// tokenizeHan segments a Han run. In simple mode (or if the dictionary
// failed to load) it falls back to single characters — a lossy but
// functional unigram index.
func (t *Tokenizer) tokenizeHan(s string) []string {
	if t.name == "simple" || t.name == "en" || t.name == "en_stem" {
		return hanUnigrams(s)
	}
	seg, err := loadGse()
	if err != nil {
		return hanUnigrams(s)
	}
	words := seg.CutSearch(s, true)
	out := make([]string, 0, len(words))
	for _, w := range words {
		if w = strings.ToLower(w); hasLetterOrDigit(w) {
			out = append(out, w)
		}
	}
	return out
}

// splitRuns splits text into maximal runs of Han characters and runs of
// everything else (Latin words, digits, punctuation, other scripts).
type textRun struct {
	text string
	han  bool
}

func splitRuns(text string) []textRun {
	var runs []textRun
	var b strings.Builder
	curHan := false
	for _, r := range text {
		h := unicode.Is(unicode.Han, r)
		if b.Len() == 0 {
			curHan = h
		} else if h != curHan {
			runs = append(runs, textRun{b.String(), curHan})
			b.Reset()
			curHan = h
		}
		b.WriteRune(r)
	}
	if b.Len() > 0 {
		runs = append(runs, textRun{b.String(), curHan})
	}
	return runs
}

// latinWords emits lowercased alphanumeric word tokens; all other runes
// act as separators. Other letter scripts (Katakana, Hangul, ...) are
// kept as letter runs.
func latinWords(s string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			out = append(out, strings.ToLower(b.String()))
			b.Reset()
		}
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

func hanUnigrams(s string) []string {
	out := make([]string, 0, len(s)/3)
	for _, r := range s {
		out = append(out, string(r))
	}
	return out
}

func hasLetterOrDigit(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

package wiki

import (
	"fmt"
	"math"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Frontmatter is a parsed YAML frontmatter map. Keys are sorted on
// serialization (matching the Rust BTreeMap behaviour).
type Frontmatter map[string]any

// ParsedPage is a markdown page split into frontmatter and body.
type ParsedPage struct {
	Frontmatter Frontmatter
	Body        string
}

// Title returns the "title" frontmatter value, if a string.
func (p *ParsedPage) Title() string { return fmString(p.Frontmatter, "title") }

// PageType returns the "type" frontmatter value, if a string.
func (p *ParsedPage) PageType() string { return fmString(p.Frontmatter, "type") }

// Status returns the "status" frontmatter value, if a string.
func (p *ParsedPage) Status() string { return fmString(p.Frontmatter, "status") }

// Tags returns the "tags" list; empty if absent.
func (p *ParsedPage) Tags() []string { return p.StringList("tags") }

// SupersededBy returns the "superseded_by" value, if present.
func (p *ParsedPage) SupersededBy() string { return fmString(p.Frontmatter, "superseded_by") }

// StringList returns a sequence field as strings; empty if absent or not a list.
func (p *ParsedPage) StringList(key string) []string {
	raw, ok := p.Frontmatter[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func fmString(fm Frontmatter, key string) string {
	s, _ := fm[key].(string)
	return s
}

// bom is the UTF-8 byte-order mark, stripped before frontmatter parsing.
var bom = string(rune(0xFEFF))

// ParseFrontmatter splits content into frontmatter and body. Missing or
// malformed frontmatter yields an empty map and the full text as body
// (matching the lenient Rust parser used at index time).
func ParseFrontmatter(content string) ParsedPage {
	trimmed := strings.TrimPrefix(content, bom)
	if !strings.HasPrefix(trimmed, "---") {
		return ParsedPage{Frontmatter: Frontmatter{}, Body: trimmed}
	}
	rest := strings.TrimPrefix(trimmed[3:], "\r")
	rest = strings.TrimPrefix(rest, "\n")

	yamlStr, afterClose, ok := splitFrontmatterBlock(rest)
	if !ok {
		return ParsedPage{Frontmatter: Frontmatter{}, Body: trimmed}
	}
	body := strings.TrimPrefix(afterClose, "\r\n")
	body = strings.TrimPrefix(body, "\n")

	fm := Frontmatter{}
	if err := yaml.Unmarshal([]byte(yamlStr), &fm); err != nil {
		return ParsedPage{Frontmatter: Frontmatter{}, Body: body}
	}
	if fm == nil {
		fm = Frontmatter{}
	}
	return ParsedPage{Frontmatter: fm, Body: body}
}

// ParseFrontmatterStrict errors when the frontmatter block is missing or
// the YAML is invalid.
func ParseFrontmatterStrict(content string) (ParsedPage, error) {
	trimmed := strings.TrimPrefix(content, bom)
	if !strings.HasPrefix(trimmed, "---") {
		return ParsedPage{}, fmt.Errorf("no frontmatter block found")
	}
	rest := strings.TrimPrefix(trimmed[3:], "\r")
	rest = strings.TrimPrefix(rest, "\n")

	yamlStr, afterClose, ok := splitFrontmatterBlock(rest)
	if !ok {
		return ParsedPage{}, fmt.Errorf("no closing --- found")
	}
	body := strings.TrimPrefix(afterClose, "\r\n")
	body = strings.TrimPrefix(body, "\n")

	fm := Frontmatter{}
	if err := yaml.Unmarshal([]byte(yamlStr), &fm); err != nil {
		return ParsedPage{}, fmt.Errorf("invalid YAML: %v", err)
	}
	if fm == nil {
		fm = Frontmatter{}
	}
	return ParsedPage{Frontmatter: fm, Body: body}, nil
}

// splitFrontmatterBlock finds the closing --- of a frontmatter block and
// returns the YAML text plus everything after the closing delimiter.
func splitFrontmatterBlock(rest string) (yamlStr, afterClose string, ok bool) {
	if strings.HasPrefix(rest, "---") { // empty frontmatter block
		return "", rest[3:], true
	}
	if pos := strings.Index(rest, "\n---"); pos >= 0 {
		return rest[:pos], rest[pos+4:], true
	}
	return "", "", false
}

// WriteFrontmatter serializes frontmatter + body back to markdown.
func WriteFrontmatter(fm Frontmatter, body string) (string, error) {
	buf, err := yaml.Marshal(fm)
	if err != nil {
		return "", fmt.Errorf("frontmatter serialization failed: %v", err)
	}
	return fmt.Sprintf("---\n%s---\n\n%s", buf, body), nil
}

// GenerateMinimalFrontmatter builds the minimum frontmatter for a file
// that has none.
func GenerateMinimalFrontmatter(title string) Frontmatter {
	return Frontmatter{
		"title":        title,
		"type":         "page",
		"status":       "active",
		"last_updated": time.Now().Format("2006-01-02"),
	}
}

// ScaffoldFrontmatter scaffolds frontmatter for a new page or section.
func ScaffoldFrontmatter(slug Slug, section bool) Frontmatter {
	fm := Frontmatter{
		"title":        slug.Title(),
		"status":       "draft",
		"last_updated": time.Now().Format("2006-01-02"),
		"type":         "page",
		"confidence":   0.5,
	}
	if section {
		fm["type"] = "section"
	}
	return fm
}

// Confidence reads the page-level confidence, mapping the legacy string
// values high/medium/low. Returns false when the page does not declare
// one — absence is meaningful and must not be conflated with 0.5.
func Confidence(fm Frontmatter) (float64, bool) {
	var v float64
	switch c := fm["confidence"].(type) {
	case int:
		v = float64(c)
	case int64:
		v = float64(c)
	case float64:
		v = c
	case float32:
		v = float64(c)
	case string:
		switch c {
		case "high":
			v = 0.9
		case "medium":
			v = 0.5
		case "low":
			v = 0.2
		default:
			return 0, false
		}
	default:
		return 0, false
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return clamp(v, 0, 1), true
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// TitleFromBodyOrFilename extracts the first "# Heading", falling back to
// the filename stem title-cased.
func TitleFromBodyOrFilename(body, filename string) string {
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if h, ok := strings.CutPrefix(t, "# "); ok {
			if h = strings.TrimSpace(h); h != "" {
				return h
			}
		}
	}
	return TitleCase(strings.TrimSuffix(filename, ".md"))
}

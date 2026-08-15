package wiki

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Slug is a validated wiki page identifier: a path relative to the wiki
// root, without extension. Invariants enforced at construction:
// non-empty, no leading "/", no ".." traversal, no hidden components
// (".*"), no file extension on the last segment.
type Slug string

// NewSlug validates and constructs a Slug.
func NewSlug(s string) (Slug, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("slug cannot be empty")
	}
	if strings.HasPrefix(s, "/") {
		return "", fmt.Errorf("slug cannot start with /: %s", s)
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == ".." || seg == "." {
			return "", fmt.Errorf("slug cannot contain path traversal: %s", s)
		}
		if strings.HasPrefix(seg, ".") {
			return "", fmt.Errorf("slug cannot contain hidden components: %s", s)
		}
		if seg == "" {
			return "", fmt.Errorf("slug cannot contain empty components: %s", s)
		}
	}
	if last := s[strings.LastIndexByte(s, '/')+1:]; last == "" {
		return "", fmt.Errorf("slug cannot have a file extension: %s", s)
	} else if dot := strings.LastIndexByte(last, '.'); dot >= 0 && dot < len(last)-1 {
		return "", fmt.Errorf("slug cannot have a file extension: %s", s)
	}
	return Slug(s), nil
}

// SlugFromPath derives a slug from a file path relative to wiki root:
// "concepts/moe.md" → "concepts/moe"; "concepts/moe/index.md" → "concepts/moe".
func SlugFromPath(p, wikiRoot string) (Slug, error) {
	rel, err := filepath.Rel(wikiRoot, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path is not under wiki root")
	}
	rel = filepath.ToSlash(rel)
	var raw string
	if path.Base(rel) == "index.md" {
		raw = path.Dir(rel)
		if raw == "." || raw == "/" {
			raw = ""
		}
	} else {
		raw = strings.TrimSuffix(rel, path.Ext(rel))
	}
	if raw == "" {
		return "", fmt.Errorf("index.md has no parent")
	}
	return NewSlug(raw)
}

// Resolve returns the on-disk path for the slug, checking the flat layout
// (<root>/<slug>.md) then the bundle layout (<root>/<slug>/index.md).
func (s Slug) Resolve(wikiRoot string) (string, error) {
	flat := filepath.Join(wikiRoot, string(s)+".md")
	if fileExists(flat) {
		return flat, nil
	}
	bundle := filepath.Join(wikiRoot, string(s), "index.md")
	if fileExists(bundle) {
		return bundle, nil
	}
	return "", fmt.Errorf("page not found for slug: %s", s)
}

// Title derives a display title from the last slug segment:
// "concepts/mixture-of-experts" → "Mixture of Experts".
func (s Slug) Title() string {
	last := s.String()
	if i := strings.LastIndexByte(last, '/'); i >= 0 {
		last = last[i+1:]
	}
	return TitleCase(last)
}

// String returns the raw slug string.
func (s Slug) String() string { return string(s) }

// TitleCase converts "mixture-of-experts" → "Mixture of Experts".
func TitleCase(segment string) string {
	words := strings.Split(segment, "-")
	for i, w := range words {
		if w == "" {
			continue
		}
		r := []rune(w)
		words[i] = strings.ToUpper(string(r[0])) + string(r[1:])
	}
	return strings.Join(words, " ")
}

// WikiUri is a parsed "wiki://" URI or bare slug.
// "wiki://research/concepts/moe" → Wiki "research", Slug "concepts/moe".
type WikiUri struct {
	Wiki string // empty for bare slugs
	Slug Slug
}

// ParseWikiUri accepts both "wiki://" URIs and bare slugs.
func ParseWikiUri(input string) (*WikiUri, error) {
	input = strings.TrimSpace(input)
	if rest, ok := strings.CutPrefix(input, "wiki://"); ok {
		if rest == "" {
			return nil, fmt.Errorf("invalid wiki URI: %s", input)
		}
		if idx := strings.IndexByte(rest, '/'); idx >= 0 && idx < len(rest)-1 {
			slug, err := NewSlug(rest[idx+1:])
			if err != nil {
				return nil, err
			}
			return &WikiUri{Wiki: rest[:idx], Slug: slug}, nil
		}
		slug, err := NewSlug(strings.TrimRight(rest, "/"))
		if err != nil {
			return nil, err
		}
		return &WikiUri{Slug: slug}, nil
	}
	slug, err := NewSlug(input)
	if err != nil {
		return nil, err
	}
	return &WikiUri{Slug: slug}, nil
}

// ReadTarget is the outcome of resolving an input for content reads.
type ReadTarget struct {
	// Path is set when the input resolved to a page.
	Path string
	// AssetParent and AssetName are set when the input resolved to a
	// co-located asset.
	AssetParent string
	AssetName   string
}

// ResolveReadTarget tries page first, then asset fallback: if the last
// segment has a non-.md extension, split into parent slug + filename.
func ResolveReadTarget(input, wikiRoot string) (*ReadTarget, error) {
	if slug, err := NewSlug(input); err == nil {
		if p, err := slug.Resolve(wikiRoot); err == nil {
			return &ReadTarget{Path: p}, nil
		}
	}
	if pos := strings.LastIndexByte(input, '/'); pos >= 0 {
		filename := input[pos+1:]
		if dot := strings.LastIndexByte(filename, '.'); dot >= 0 && dot < len(filename)-1 && filename[dot+1:] != "md" {
			parent := input[:pos]
			p := filepath.Join(wikiRoot, parent, filename)
			if fileExists(p) {
				return &ReadTarget{AssetParent: parent, AssetName: filename}, nil
			}
			return nil, fmt.Errorf("asset not found: %s", input)
		}
	}
	return nil, fmt.Errorf("page not found: %s", input)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

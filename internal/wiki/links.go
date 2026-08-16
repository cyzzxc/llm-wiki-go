package wiki

import "strings"

// ParsedLink is a link value from a frontmatter edge field or a body
// [[wikilink]], classified by scope.
type ParsedLink struct {
	// Wiki is the target wiki name for "wiki://name/slug" URIs; empty for local slugs.
	Wiki string
	// Slug is the slug portion (local slug, or the slug segment of a cross-wiki URI).
	Slug string
	// CrossWiki is true when the link is a "wiki://" URI.
	CrossWiki bool
}

// ParseParsedLink classifies a raw link string.
func ParseParsedLink(s string) ParsedLink {
	if rest, ok := strings.CutPrefix(s, "wiki://"); ok {
		if idx := strings.IndexByte(rest, '/'); idx >= 0 {
			return ParsedLink{Wiki: rest[:idx], Slug: rest[idx+1:], CrossWiki: true}
		}
	}
	return ParsedLink{Slug: s}
}

// Raw returns the local slug portion (cross-wiki links return their slug).
func (l ParsedLink) Raw() string { return l.Slug }

// ExtractParsedLinks returns all links from a page: frontmatter sources,
// concepts, and body wikilinks/CommonMark links, deduplicated in order.
func ExtractParsedLinks(page *ParsedPage) []ParsedLink {
	var result []ParsedLink
	seen := map[string]bool{}
	for _, slug := range page.StringList("sources") {
		if !seen[slug] {
			seen[slug] = true
			result = append(result, ParseParsedLink(slug))
		}
	}
	for _, slug := range page.StringList("concepts") {
		if !seen[slug] {
			seen[slug] = true
			result = append(result, ParseParsedLink(slug))
		}
	}
	extractWikilinks(page.Body, seen, &result, nil)
	return result
}

// ExtractLinks is ExtractParsedLinks returning raw link strings.
func ExtractLinks(page *ParsedPage) []string {
	parsed := ExtractParsedLinks(page)
	out := make([]string, 0, len(parsed))
	for _, l := range parsed {
		out = append(out, l.Raw())
	}
	return out
}

// ExtractBodyWikilinks extracts only [[wikilinks]] and CommonMark links
// from raw body text. sourceDir normalizes relative CommonMark
// destinations; nil leaves them unchanged.
func ExtractBodyWikilinks(text string, sourceDir []string) []string {
	var parsed []ParsedLink
	seen := map[string]bool{}
	extractWikilinks(text, seen, &parsed, sourceDir)
	out := make([]string, 0, len(parsed))
	for _, l := range parsed {
		out = append(out, l.Raw())
	}
	return out
}

func extractWikilinks(text string, seen map[string]bool, result *[]ParsedLink, sourceDir []string) {
	rest := text
	for {
		start := strings.Index(rest, "[[")
		if start < 0 {
			break
		}
		after := rest[start+2:]
		end := strings.Index(after, "]]")
		if end < 0 {
			break
		}
		raw := strings.TrimSpace(after[:end])
		if raw != "" && !seen[raw] {
			seen[raw] = true
			*result = append(*result, ParseParsedLink(raw))
		}
		rest = after[end+2:]
	}
	extractCommonmarkLinks(text, seen, result, sourceDir)
}

// NormalizeCommonmarkDest normalizes a CommonMark link destination against
// the source page's directory: strip .md, resolve ./ and ../ prefixes.
// Absolute destinations are returned unchanged (minus .md).
func NormalizeCommonmarkDest(dest, sourceDir string) string {
	dest = strings.TrimSuffix(dest, ".md")
	if !strings.HasPrefix(dest, "./") && !strings.HasPrefix(dest, "../") && dest != ".." {
		return dest
	}
	parts := strings.FieldsFunc(sourceDir, func(r rune) bool { return r == '/' })
	rest := dest
	for {
		if r, ok := strings.CutPrefix(rest, "./"); ok {
			rest = r
		} else if r, ok := strings.CutPrefix(rest, "../"); ok {
			if len(parts) > 0 {
				parts = parts[:len(parts)-1]
			}
			rest = r
		} else if rest == ".." {
			if len(parts) > 0 {
				parts = parts[:len(parts)-1]
			}
			rest = ""
			break
		} else {
			break
		}
	}
	if rest == "" {
		return strings.Join(parts, "/")
	}
	if prefix := strings.Join(parts, "/"); prefix == "" {
		return rest
	} else {
		return prefix + "/" + rest
	}
}

// extractCommonmarkLinks extracts inline [text](destination) links,
// filtering external URLs, anchors, and images; strips #anchors.
func extractCommonmarkLinks(text string, seen map[string]bool, result *[]ParsedLink, sourceDir []string) {
	rest := text
	for {
		bracket := strings.Index(rest, "](")
		if bracket < 0 {
			break
		}
		before := rest[:bracket]
		open := strings.LastIndexByte(before, '[')
		if open >= 0 {
			isImage := open > 0 && before[open-1] == '!'
			afterParen := rest[bracket+2:]
			close := strings.IndexByte(afterParen, ')')
			if close >= 0 {
				destRaw := strings.TrimSpace(afterParen[:close])
				if idx := strings.IndexByte(destRaw, '#'); idx >= 0 {
					destRaw = strings.TrimSpace(destRaw[:idx])
				}
				if !isImage && destRaw != "" &&
					!strings.HasPrefix(destRaw, "http://") &&
					!strings.HasPrefix(destRaw, "https://") &&
					!strings.HasPrefix(destRaw, "mailto:") &&
					!strings.HasPrefix(destRaw, "#") {
					raw := destRaw
					if sourceDir != nil {
						raw = NormalizeCommonmarkDest(destRaw, sourceDir[0])
					}
					if !seen[raw] {
						seen[raw] = true
						*result = append(*result, ParseParsedLink(raw))
					}
				}
				rest = afterParen[close+1:]
				continue
			}
		}
		rest = rest[bracket+2:]
	}
}

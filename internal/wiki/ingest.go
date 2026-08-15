package wiki

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// IngestOptions parameterize the ingest pipeline.
type IngestOptions struct {
	DryRun       bool
	AutoCommit   bool
	ChangedPaths map[string]bool // wiki-relative paths to validate; nil/empty = all
	Redact       *RedactConfig
}

// RedactionMatch records one redacted pattern occurrence.
type RedactionMatch struct {
	PatternName string `json:"pattern_name"`
	LineNumber  int    `json:"line_number"`
}

// RedactionReport records per-slug redactions.
type RedactionReport struct {
	Slug    string           `json:"slug"`
	Matches []RedactionMatch `json:"matches"`
}

// IngestReport is the outcome of an ingest run.
type IngestReport struct {
	PagesValidated int               `json:"pages_validated"`
	AssetsFound    int               `json:"assets_found"`
	Warnings       []string          `json:"warnings"`
	Commit         string            `json:"commit"`
	UnchangedCount int               `json:"unchanged_count"`
	Redacted       []RedactionReport `json:"redacted"`
}

// Ingest validates (and optionally commits) files under a wiki path.
func Ingest(path string, opts IngestOptions, wikiRoot string, registry *TypeRegistry, validation ValidationConfig) (*IngestReport, error) {
	repoRoot := filepath.Dir(wikiRoot)
	report := &IngestReport{Warnings: []string{}, Redacted: []RedactionReport{}}

	full := path
	if !filepath.IsAbs(full) {
		full = filepath.Join(wikiRoot, path)
	}
	if !fileExists(full) && !dirExists(full) {
		return nil, fmt.Errorf("path does not exist: %s", full)
	}
	absWiki, err := filepath.Abs(wikiRoot)
	if err == nil {
		if abs, err := filepath.Abs(full); err == nil {
			if !strings.HasPrefix(abs, absWiki) {
				return nil, fmt.Errorf("path is outside wiki root")
			}
		}
	}

	var files []string
	if fileExists(full) {
		files = append(files, full)
	} else {
		filepath.WalkDir(full, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(p, ".md") {
				report.AssetsFound++
				return nil
			}
			if opts.ChangedPaths != nil && len(opts.ChangedPaths) > 0 {
				rel, err := filepath.Rel(wikiRoot, p)
				if err != nil || !opts.ChangedPaths[filepath.ToSlash(rel)] {
					report.UnchangedCount++
					return nil
				}
			}
			files = append(files, p)
			return nil
		})
	}

	for _, f := range files {
		validateFile(f, wikiRoot, opts, registry, validation, report)
	}

	if !opts.DryRun && opts.AutoCommit {
		hash, err := GitCommit(repoRoot, fmt.Sprintf("ingest: %s — +%d pages, +%d assets", path, report.PagesValidated, report.AssetsFound))
		if err != nil {
			return report, err
		}
		report.Commit = hash
	}
	return report, nil
}

func validateFile(path, wikiRoot string, opts IngestOptions, registry *TypeRegistry, validation ValidationConfig, report *IngestReport) {
	raw, err := os.ReadFile(path)
	if err != nil {
		report.Warnings = append(report.Warnings, fmt.Sprintf("%s: cannot read (%v)", path, err))
		return
	}
	content := normalizeLineEndings(string(raw))

	if opts.Redact != nil {
		var redacted string
		var matches []RedactionMatch
		redacted, matches = RedactBody(content, opts.Redact)
		if len(matches) > 0 {
			slug, _ := SlugFromPath(path, wikiRoot)
			report.Redacted = append(report.Redacted, RedactionReport{
				Slug: slug.String(), Matches: matches,
			})
			content = redacted
			if !opts.DryRun {
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					report.Warnings = append(report.Warnings, fmt.Sprintf("%s: redaction write failed (%v)", path, err))
				}
			}
		}
	}

	page := ParseFrontmatter(content)
	if len(page.Frontmatter) == 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf("%s: no frontmatter found", path))
		report.PagesValidated++
		return
	}
	warnings, err := registry.ValidateType(page.Frontmatter, validation.TypeStrictness)
	if err != nil {
		report.Warnings = append(report.Warnings, fmt.Sprintf("%s: %v", path, err))
		return
	}
	for _, w := range warnings {
		report.Warnings = append(report.Warnings, fmt.Sprintf("%s: %s", path, w))
	}
	report.PagesValidated++
}

func normalizeLineEndings(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// ── Redaction ────────────────────────────────────────────────────────────────

type redactPattern struct {
	name        string
	re          *regexp.Regexp
	replacement string
}

// CompileRedactPatterns merges built-ins (minus disabled) with custom config.
func CompileRedactPatterns(cfg *RedactConfig) []redactPattern {
	builtins := []struct {
		name, pattern, replacement string
	}{
		{"github-pat", `ghp_[A-Za-z0-9]{36}`, "[REDACTED:github-pat]"},
		{"openai-key", `sk-[A-Za-z0-9]{48}`, "[REDACTED:openai-key]"},
		{"anthropic-key", `sk-ant-[A-Za-z0-9\-]{90,}`, "[REDACTED:anthropic-key]"},
		{"aws-access-key", `AKIA[0-9A-Z]{16}`, "[REDACTED:aws-access-key]"},
		{"bearer-token", `Bearer [A-Za-z0-9\-._~+/]{20,}`, "[REDACTED:bearer-token]"},
		{"email", `[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`, "[REDACTED:email]"},
	}
	disabled := map[string]bool{}
	if cfg != nil {
		for _, d := range cfg.Disable {
			disabled[d] = true
		}
	}
	var out []redactPattern
	for _, b := range builtins {
		if disabled[b.name] {
			continue
		}
		if re, err := regexp.Compile(b.pattern); err == nil {
			out = append(out, redactPattern{b.name, re, b.replacement})
		}
	}
	if cfg != nil {
		for _, cp := range cfg.Patterns {
			re, err := regexp.Compile(cp.Pattern)
			if err != nil {
				continue
			}
			out = append(out, redactPattern{cp.Name, re, cp.Replacement})
		}
	}
	return out
}

// RedactBody applies redaction line-by-line to the body only (frontmatter
// untouched), reporting 1-based line numbers.
func RedactBody(content string, cfg *RedactConfig) (string, []RedactionMatch) {
	patterns := CompileRedactPatterns(cfg)
	if len(patterns) == 0 {
		return content, nil
	}
	lines := strings.Split(content, "\n")
	var matches []RedactionMatch
	fmLines := frontmatterLineCount(content)
	for i := range lines {
		if i < fmLines {
			continue // only redact the body
		}
		for _, p := range patterns {
			if p.re.MatchString(lines[i]) {
				matches = append(matches, RedactionMatch{PatternName: p.name, LineNumber: i + 1})
				lines[i] = p.re.ReplaceAllString(lines[i], p.replacement)
			}
		}
	}
	hadTrailing := strings.HasSuffix(content, "\n")
	out := strings.Join(lines, "\n")
	if hadTrailing && !strings.HasSuffix(out, "\n") {
		out += "\n"
	} else if !hadTrailing && strings.HasSuffix(out, "\n") {
		out = strings.TrimSuffix(out, "\n")
	}
	return out, matches
}

func frontmatterLineCount(content string) int {
	// lines before the closing delimiter, plus the opening and closing lines
	trimmed := strings.TrimPrefix(content, bom)
	if !strings.HasPrefix(trimmed, "---") {
		return 0
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(trimmed[3:], "\r"), "\n")
	if pos := strings.Index(rest, "\n---"); pos >= 0 {
		return 2 + strings.Count(rest[:pos], "\n") // opening line + yaml lines + closing line
	}
	return 0
}

package wiki

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ReadPage reads a page by slug, optionally stripping frontmatter, and
// appends a supersession notice when superseded_by is set.
func ReadPage(slug Slug, wikiRoot string, noFrontmatter bool) (string, error) {
	path, err := slug.Resolve(wikiRoot)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	content := string(raw)
	page := ParseFrontmatter(content)

	out := content
	if noFrontmatter {
		out = page.Body
	}
	if s := page.SupersededBy(); s != "" {
		out += fmt.Sprintf("\n> **Superseded** by [%s](wiki://%s)\n", s, s)
	}
	return out, nil
}

// WritePageResult reports a page write outcome.
type WritePageResult struct {
	BytesWritten int
	Path         string
}

// WritePage writes content to the path resolved from slug, creating
// parent directories. Does not validate or commit.
func WritePage(slugStr, content, wikiRoot string) (WritePageResult, error) {
	if slug, err := NewSlug(slugStr); err == nil {
		if path, err := slug.Resolve(wikiRoot); err == nil {
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return WritePageResult{}, err
			}
			return WritePageResult{BytesWritten: len(content), Path: path}, nil
		}
	}
	path := filepath.Join(wikiRoot, slugStr+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return WritePageResult{}, err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return WritePageResult{}, err
	}
	return WritePageResult{BytesWritten: len(content), Path: path}, nil
}

// ListAssets lists co-located assets of a bundle page as wiki:// URIs.
func ListAssets(slug Slug, wikiRoot string) ([]string, error) {
	bundleDir := filepath.Join(wikiRoot, slug.String())
	if !dirExists(bundleDir) || !fileExists(filepath.Join(bundleDir, "index.md")) {
		return nil, nil
	}
	entries, err := os.ReadDir(bundleDir)
	if err != nil {
		return nil, err
	}
	var assets []string
	for _, e := range entries {
		name := e.Name()
		if name != "index.md" && e.Type().IsRegular() {
			assets = append(assets, fmt.Sprintf("wiki://%s/%s", slug, name))
		}
	}
	sort.Strings(assets)
	return assets, nil
}

// ReadAsset reads raw bytes of a co-located asset.
func ReadAsset(slug Slug, filename, wikiRoot string) ([]byte, error) {
	path := filepath.Join(wikiRoot, slug.String(), filename)
	if !fileExists(path) {
		return nil, fmt.Errorf("asset not found: %s/%s", slug, filename)
	}
	return os.ReadFile(path)
}

// CreatePage creates a new page with scaffolded frontmatter, overriding
// title/type when given, seeding the body with a template. Auto-creates
// missing parent sections (type: section).
func CreatePage(slug Slug, bundle bool, wikiRoot string, nameOverride, typeOverride, bodyTemplate string) (string, error) {
	slugStr := slug.String()

	parts := strings.Split(slugStr, "/")
	for i := 1; i < len(parts); i++ {
		parentSlug := strings.Join(parts[:i], "/")
		parentDir := filepath.Join(wikiRoot, parentSlug)
		if !dirExists(parentDir) {
			if err := os.MkdirAll(parentDir, 0o755); err != nil {
				return "", err
			}
			parentS, err := NewSlug(parentSlug)
			if err != nil {
				return "", err
			}
			content, err := WriteFrontmatter(ScaffoldFrontmatter(parentS, true), "")
			if err != nil {
				return "", err
			}
			if err := os.WriteFile(filepath.Join(parentDir, "index.md"), []byte(content), 0o644); err != nil {
				return "", err
			}
		}
	}

	fm := ScaffoldFrontmatter(slug, false)
	if nameOverride != "" {
		fm["title"] = nameOverride
	}
	if typeOverride != "" {
		fm["type"] = typeOverride
	}
	content, err := WriteFrontmatter(fm, bodyTemplate)
	if err != nil {
		return "", err
	}

	if bundle {
		dir := filepath.Join(wikiRoot, slugStr)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		p := filepath.Join(dir, "index.md")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return "", err
		}
		return p, nil
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(wikiRoot, slugStr)), 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(wikiRoot, slugStr+".md")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return "", err
	}
	return p, nil
}

// CreateSection creates a directory + index.md with type: section.
func CreateSection(slug Slug, wikiRoot, bodyTemplate string) (string, error) {
	dir := filepath.Join(wikiRoot, slug.String())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	content, err := WriteFrontmatter(ScaffoldFrontmatter(slug, true), bodyTemplate)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "index.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// PromoteToBundle moves a flat page into slug/index.md.
func PromoteToBundle(slug Slug, wikiRoot string) error {
	flat := filepath.Join(wikiRoot, slug.String()+".md")
	if !fileExists(flat) {
		return fmt.Errorf("flat page not found for slug: %s", slug)
	}
	bundleDir := filepath.Join(wikiRoot, slug.String())
	dest := filepath.Join(bundleDir, "index.md")
	if fileExists(dest) {
		return fmt.Errorf("bundle already exists at %s; remove it manually before promoting", dest)
	}
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		return err
	}
	return os.Rename(flat, dest)
}

// DeletePage removes a page (flat or bundle). Returns false when the page
// was not found.
func DeletePage(slugStr, wikiRoot string) (bool, error) {
	flat := filepath.Join(wikiRoot, slugStr+".md")
	if fileExists(flat) {
		return true, os.Remove(flat)
	}
	bundle := filepath.Join(wikiRoot, slugStr)
	if fileExists(filepath.Join(bundle, "index.md")) {
		return true, os.RemoveAll(bundle)
	}
	return false, nil
}

// StripFrontmatter removes the frontmatter block from raw content;
// unclosed blocks pass through unchanged.
func StripFrontmatter(content string) string {
	trimmed := strings.TrimPrefix(content, bom)
	if !strings.HasPrefix(trimmed, "---") {
		return content
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(trimmed[3:], "\r"), "\n")
	if pos := strings.Index(rest, "\n---"); pos >= 0 {
		after := rest[pos+4:]
		after = strings.TrimPrefix(after, "\r\n")
		after = strings.TrimPrefix(after, "\n")
		return after
	}
	return content
}

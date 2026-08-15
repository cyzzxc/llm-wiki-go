package wiki

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"llm-wiki-go/internal/assets"
)

// ContentReadResult is the outcome of a content read: a page, an asset
// listing, or a binary asset marker.
type ContentReadResult struct {
	Kind    ContentKind
	Content string   // Page text
	Assets  []string // Assets listing
}

// ContentKind discriminates ContentReadResult.
type ContentKind int

// Content kinds.
const (
	ContentPage ContentKind = iota
	ContentAssets
	ContentBinary
)

// ContentRead reads a page (or assets) by URI, applying resolved config.
func ContentRead(engine *WikiEngine, uri, wikiFlag string, noFrontmatter, listAssets bool) (*ContentReadResult, error) {
	space, slug, err := resolveUriTarget(engine, uri, wikiFlag)
	if err != nil {
		return nil, err
	}
	if listAssets {
		assets, err := ListAssets(slug, space.WikiRoot)
		if err != nil {
			return nil, err
		}
		return &ContentReadResult{Kind: ContentAssets, Assets: assets}, nil
	}
	target, err := ResolveReadTarget(slug.String(), space.WikiRoot)
	if err != nil {
		return nil, err
	}
	if target.Path != "" {
		strip := noFrontmatter || space.Resolved.Read.NoFrontmatter
		content, err := ReadPage(slug, space.WikiRoot, strip)
		if err != nil {
			return nil, err
		}
		return &ContentReadResult{Kind: ContentPage, Content: content}, nil
	}
	raw, err := ReadAsset(mustSlug(target.AssetParent), target.AssetName, space.WikiRoot)
	if err != nil {
		return nil, err
	}
	if utf8.Valid(raw) {
		return &ContentReadResult{Kind: ContentPage, Content: string(raw)}, nil
	}
	return &ContentReadResult{Kind: ContentBinary}, nil
}

// ContentWrite writes content to a page by URI.
func ContentWrite(engine *WikiEngine, uri, content, wikiFlag string) (WritePageResult, *SpaceContext, error) {
	space, slug, err := resolveUriTarget(engine, uri, wikiFlag)
	if err != nil {
		return WritePageResult{}, nil, err
	}
	res, err := WritePage(slug.String(), content, space.WikiRoot)
	return res, space, err
}

// ContentNewResult reports a page creation.
type ContentNewResult struct {
	URI      string `json:"uri"`
	Slug     string `json:"slug"`
	Path     string `json:"path"`
	WikiRoot string `json:"wiki_root"`
	Bundle   bool   `json:"bundle"`
}

// ContentNew creates a page or section with scaffolded frontmatter.
func ContentNew(engine *WikiEngine, uri, wikiFlag string, section, bundle bool, name, typeOverride string) (*ContentNewResult, error) {
	space, slug, err := resolveUriTarget(engine, uri, wikiFlag)
	if err != nil {
		return nil, err
	}
	typeName := "page"
	if section {
		typeName = "section"
	} else if typeOverride != "" {
		typeName = typeOverride
	}
	body, err := resolveBodyTemplate(space.RepoRoot, typeName)
	if err != nil {
		return nil, err
	}

	var path string
	if section {
		path, err = CreateSection(slug, space.WikiRoot, body)
	} else {
		if name == "" {
			name = ""
		}
		path, err = CreatePage(slug, bundle, space.WikiRoot, name, typeOverride, body)
	}
	if err != nil {
		return nil, err
	}
	return &ContentNewResult{
		URI:      fmt.Sprintf("wiki://%s/%s", space.Name, slug),
		Slug:     slug.String(),
		Path:     path,
		WikiRoot: space.WikiRoot,
		Bundle:   bundle && !section,
	}, nil
}

// resolveBodyTemplate finds schemas/<type>.md in the repo, then the
// embedded default template.
func resolveBodyTemplate(repoRoot, typeName string) (string, error) {
	if strings.Contains(typeName, "..") {
		return "", fmt.Errorf("invalid type name: %s", typeName)
	}
	local := filepath.Join(repoRoot, "schemas", typeName+".md")
	if raw, err := os.ReadFile(local); err == nil {
		return string(raw), nil
	}
	if raw := embeddedTemplate(typeName); raw != nil {
		return string(raw), nil
	}
	return "", nil
}

// ContentCommit commits specific slugs (or all) to git.
func ContentCommit(engine *WikiEngine, wikiFlag string, slugs []string, all bool, message string) (string, error) {
	if len(slugs) == 0 && !all {
		return "", fmt.Errorf("specify slugs or --all")
	}
	space, err := engine.ResolveWiki(wikiFlag)
	if err != nil {
		return "", err
	}
	if all {
		msg := message
		if msg == "" {
			msg = "commit: all"
		}
		return GitCommit(space.RepoRoot, msg)
	}
	var paths []string
	for _, slugStr := range slugs {
		slug, err := NewSlug(slugStr)
		if err != nil {
			return "", err
		}
		path, err := slug.Resolve(space.WikiRoot)
		if err != nil {
			return "", err
		}
		if filepath.Base(path) == "index.md" {
			// expand bundles to every file in the directory
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil {
				return "", err
			}
			for _, e := range entries {
				if !e.IsDir() {
					paths = append(paths, filepath.Join(filepath.Dir(path), e.Name()))
				}
			}
			continue
		}
		paths = append(paths, path)
	}
	msg := message
	if msg == "" {
		msg = fmt.Sprintf("commit: %s", strings.Join(slugs, ", "))
	}
	return GitCommitPaths(space.RepoRoot, paths, msg)
}

// ResolveResult is the wiki_resolve tool output.
type ResolveResult struct {
	Slug     string `json:"slug"`
	Wiki     string `json:"wiki"`
	WikiRoot string `json:"wiki_root"`
	Path     string `json:"path"`
	Exists   bool   `json:"exists"`
	Bundle   bool   `json:"bundle"`
}

// ResolveUriToPath resolves a URI to its filesystem path (use before
// writing content directly to disk).
func ResolveUriToPath(engine *WikiEngine, uri, wikiFlag string) (*ResolveResult, error) {
	space, slug, err := resolveUriTarget(engine, uri, wikiFlag)
	if err != nil {
		return nil, err
	}
	res := &ResolveResult{
		Slug:     slug.String(),
		Wiki:     space.Name,
		WikiRoot: space.WikiRoot,
		Path:     filepath.Join(space.WikiRoot, slug.String()+".md"),
	}
	if p, err := slug.Resolve(space.WikiRoot); err == nil {
		res.Path = p
		res.Exists = true
		res.Bundle = filepath.Base(p) == "index.md"
	}
	return res, nil
}

// resolveUriTarget mirrors WikiUri::resolve: wiki:// URIs try the wiki
// name then fall back to the default wiki with a longer slug.
func resolveUriTarget(engine *WikiEngine, input, wikiFlag string) (*SpaceContext, Slug, error) {
	uri, err := ParseWikiUri(input)
	if err != nil {
		return nil, "", err
	}
	if uri.Wiki != "" {
		if space, err := engine.Space(uri.Wiki); err == nil {
			return space, uri.Slug, nil
		}
		full, err := NewSlug(uri.Wiki + "/" + uri.Slug.String())
		if err != nil {
			return nil, "", err
		}
		space, err := engine.ResolveWiki("")
		if err != nil {
			return nil, "", err
		}
		return space, full, nil
	}
	space, err := engine.ResolveWiki(wikiFlag)
	if err != nil {
		return nil, "", err
	}
	return space, uri.Slug, nil
}

func mustSlug(s string) Slug {
	slug, _ := NewSlug(s)
	return slug
}

// embeddedTemplate returns the embedded default body template for a type.
func embeddedTemplate(typeName string) []byte {
	return assets.Schema(typeName + ".md")
}

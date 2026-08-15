package wiki

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"llm-wiki-go/internal/assets"
)

// ── Config ops ───────────────────────────────────────────────────────────────

// OpsConfigGet reads a dot-notation key for the resolved wiki config.
func OpsConfigGet(engine *WikiEngine, wikiFlag, key string) (string, error) {
	space, err := engine.ResolveWiki(wikiFlag)
	if err != nil {
		return "", err
	}
	return GetConfigValue(&space.Resolved, engine.State.Config, key), nil
}

// OpsConfigSet writes a dot-notation key to the global or per-wiki config.
func OpsConfigSet(engine *WikiEngine, wikiFlag, key, value string, globalWrite bool) (string, error) {
	if globalWrite {
		if err := SetGlobalConfigValue(engine.State.Config, key, value); err != nil {
			return "", err
		}
		if err := SaveGlobal(engine.State.Config, engine.State.ConfigPath); err != nil {
			return "", err
		}
		return fmt.Sprintf("Set %s = %s (global)", key, value), nil
	}
	name := wikiFlag
	if name == "" {
		name = engine.State.Config.Global.DefaultWiki
	}
	space, err := engine.Space(name)
	if err != nil {
		return "", err
	}
	wikiCfg, err := LoadWiki(space.RepoRoot)
	if err != nil {
		return "", err
	}
	if err := SetWikiConfigValue(wikiCfg, key, value); err != nil {
		return "", err
	}
	if err := SaveWiki(wikiCfg, space.RepoRoot); err != nil {
		return "", err
	}
	// hot-reload resolved config for this space
	wikiCfg, _ = LoadWiki(space.RepoRoot)
	space.Resolved = Resolve(engine.State.Config, wikiCfg)
	return fmt.Sprintf("Set %s = %s (wiki: %s)", key, value, name), nil
}

// OpsConfigListGlobal renders the raw global config TOML.
func OpsConfigListGlobal(engine *WikiEngine) (string, error) {
	raw, err := os.ReadFile(engine.State.ConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "# (no config file — defaults in effect)\n", nil
		}
		return "", err
	}
	return string(raw), nil
}

// ── Index ops ────────────────────────────────────────────────────────────────

// OpsIndexRebuild rebuilds a space's index and graph caches.
func OpsIndexRebuild(engine *WikiEngine, wikiName string) (*IndexReport, error) {
	space, err := engine.Space(wikiName)
	if err != nil {
		return nil, err
	}
	report, err := space.IndexManager.Rebuild(space.WikiRoot, space.RepoRoot, space.IndexSchema, space.TypeRegistry)
	if err != nil {
		return nil, err
	}
	space.GraphCache.Rebuild(space.IndexManager.Generation(), func() (*WikiGraph, error) {
		ix := space.IndexManager.Searcher()
		if ix == nil {
			return NewWikiGraph(), nil
		}
		return BuildGraph(ix, GraphFilter{}, space.TypeRegistry), nil
	})
	return &report, nil
}

// OpsIndexStatus returns index health for a space.
func OpsIndexStatus(engine *WikiEngine, wikiName string) (*IndexStatus, *SpaceContext, error) {
	space, err := engine.Space(wikiName)
	if err != nil {
		return nil, nil, err
	}
	return space.IndexManager.Status(space.RepoRoot), space, nil
}

// ── Spaces ops (hot-mount variants used by MCP) ──────────────────────────────

// OpsSpacesCreate creates and hot-mounts a wiki.
func OpsSpacesCreate(engine *WikiEngine, path, name, description string, force, setAsDefault bool, wikiRoot string) (*SpacesCreateReport, error) {
	report, err := SpacesCreate(path, name, description, force, setAsDefault, engine.State.ConfigPath, wikiRoot)
	if err != nil {
		return nil, err
	}
	engine.State.Config, _ = LoadGlobal(engine.State.ConfigPath)
	if entry, err := ResolveWikiName(name, engine.State.Config); err == nil {
		engine.MountWiki(entry)
	}
	return report, nil
}

// OpsSpacesRegister registers and hot-mounts an existing wiki.
func OpsSpacesRegister(engine *WikiEngine, path, name, description, wikiRoot string) (*SpacesRegisterReport, error) {
	report, err := SpacesRegisterExisting(path, name, description, wikiRoot, engine.State.ConfigPath)
	if err != nil {
		return nil, err
	}
	engine.State.Config, _ = LoadGlobal(engine.State.ConfigPath)
	if entry, err := ResolveWikiName(name, engine.State.Config); err == nil {
		engine.MountWiki(entry)
	}
	return report, nil
}

// OpsSpacesRemove unmounts and unregisters a wiki.
func OpsSpacesRemove(engine *WikiEngine, name string, del bool) error {
	if err := engine.UnmountWiki(name); err != nil {
		return err
	}
	if err := RemoveWiki(name, del, engine.State.ConfigPath); err != nil {
		return err
	}
	engine.State.Config, _ = LoadGlobal(engine.State.ConfigPath)
	return nil
}

// OpsSpacesSetDefault updates the default wiki (config + engine).
func OpsSpacesSetDefault(engine *WikiEngine, name string) error {
	if err := SetDefaultWiki(name, engine.State.ConfigPath); err != nil {
		return err
	}
	return engine.SetDefault(name)
}

// ── Schema add/remove ops ────────────────────────────────────────────────────

// OpsSchemaAdd validates and installs a schema file, registering the type
// in wiki.toml and rebuilding the index.
func OpsSchemaAdd(engine *WikiEngine, wikiName, typeName, schemaPath string) (string, error) {
	space, err := engine.Space(wikiName)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %v", schemaPath, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("invalid JSON in %s: %v", schemaPath, err)
	}
	wt, _ := doc["x-wiki-types"].(map[string]any)
	if _, ok := wt[typeName]; !ok {
		return "", fmt.Errorf("schema does not declare type %q in x-wiki-types", typeName)
	}
	// compile check
	if _, _, err := BuildSpace(space.RepoRoot); err != nil {
		// pre-existing state is fine; only new-file errors matter here
		if strings.Contains(err.Error(), filepath.Base(schemaPath)) {
			return "", err
		}
	}

	dest := filepath.Join(space.RepoRoot, "schemas", filepath.Base(schemaPath))
	if err := os.WriteFile(dest, raw, 0o644); err != nil {
		return "", err
	}
	msg := fmt.Sprintf("copied to %s", dest)

	wikiCfg, err := LoadWiki(space.RepoRoot)
	if err != nil {
		return "", err
	}
	needToml := true
	for name, te := range wikiCfg.Types {
		_ = name
		if te.Schema == filepath.Join("schemas", filepath.Base(schemaPath)) {
			needToml = false
			break
		}
	}
	if wt[typeName] != nil {
		if _, declared := wt[typeName]; declared {
			needToml = false
		}
	}
	if needToml {
		if wikiCfg.Types == nil {
			wikiCfg.Types = map[string]TypeEntry{}
		}
		wikiCfg.Types[typeName] = TypeEntry{
			Schema:      filepath.Join("schemas", filepath.Base(schemaPath)),
			Description: fmt.Sprintf("Custom type: %s", typeName),
		}
		if err := SaveWiki(wikiCfg, space.RepoRoot); err != nil {
			return "", err
		}
		msg += fmt.Sprintf(", added [types.%s] to wiki.toml", typeName)
	}

	// remount with the new schema + rebuild index
	if entry, err := ResolveWikiName(wikiName, engine.State.Config); err == nil {
		if err := engine.MountWiki(entry); err == nil {
			if s2, err := engine.Space(wikiName); err == nil {
				s2.IndexManager.Rebuild(s2.WikiRoot, s2.RepoRoot, s2.IndexSchema, s2.TypeRegistry)
				msg += ", search index rebuilt"
			}
		}
	}
	return msg, nil
}

// SchemaRemoveReport reports a schema removal.
type SchemaRemoveReport struct {
	PagesRemoved       int  `json:"pages_removed"`
	PagesDeletedOnDisk int  `json:"pages_deleted_from_disk"`
	WikiTomlUpdated    bool `json:"wiki_toml_updated"`
	SchemaFileDeleted  bool `json:"schema_file_deleted"`
	DryRun             bool `json:"dry_run"`
}

// OpsSchemaRemove removes a type: drops its pages from the index,
// optionally deletes page files, updates wiki.toml, deletes the schema
// file when it declares only that type, and commits.
func OpsSchemaRemove(engine *WikiEngine, wikiName, typeName string, delSchema, delPages, dryRun bool) (*SchemaRemoveReport, error) {
	if typeName == "default" {
		return nil, fmt.Errorf("cannot remove the 'default' type")
	}
	space, err := engine.Space(wikiName)
	if err != nil {
		return nil, err
	}
	t, ok := space.TypeRegistry.Types[typeName]
	if !ok {
		return nil, fmt.Errorf("type '%s' is not registered", typeName)
	}
	report := &SchemaRemoveReport{DryRun: dryRun}

	ix := space.IndexManager.Searcher()
	var pagePaths []string
	if ix != nil {
		for _, d := range ix.Docs {
			if d.Type != typeName {
				continue
			}
			report.PagesRemoved++
			if p, err := mustSlug(d.Slug).Resolve(space.WikiRoot); err == nil {
				pagePaths = append(pagePaths, p)
			}
		}
	}
	if dryRun {
		report.PagesDeletedOnDisk = len(pagePaths)
		return report, nil
	}

	if delPages {
		for _, p := range pagePaths {
			if err := os.Remove(p); err == nil {
				report.PagesDeletedOnDisk++
			} else if os.IsNotExist(err) {
				// already gone
			}
		}
	}

	wikiCfg, err := LoadWiki(space.RepoRoot)
	if err != nil {
		return nil, err
	}
	if _, ok := wikiCfg.Types[typeName]; ok {
		delete(wikiCfg.Types, typeName)
		if err := SaveWiki(wikiCfg, space.RepoRoot); err != nil {
			return nil, err
		}
		report.WikiTomlUpdated = true
	}

	// delete schema file only when it declares ≤1 type
	if delSchema {
		if raw, err := os.ReadFile(t.SchemaPath); err == nil {
			var doc map[string]any
			if json.Unmarshal(raw, &doc) == nil {
				if wt, ok := doc["x-wiki-types"].(map[string]any); ok && len(wt) <= 1 {
					if err := os.Remove(t.SchemaPath); err == nil {
						report.SchemaFileDeleted = true
					}
				}
			}
		}
	}

	if report.PagesRemoved > 0 || report.WikiTomlUpdated || report.SchemaFileDeleted {
		GitCommit(space.RepoRoot, fmt.Sprintf("schema remove: %s — %d pages, wiki.toml=%v, schema=%v",
			typeName, report.PagesRemoved, report.WikiTomlUpdated, report.SchemaFileDeleted))
	}

	// remount + rebuild
	if entry, err := ResolveWikiName(wikiName, engine.State.Config); err == nil {
		if err := engine.MountWiki(entry); err == nil {
			if s2, err := engine.Space(wikiName); err == nil {
				s2.IndexManager.Rebuild(s2.WikiRoot, s2.RepoRoot, s2.IndexSchema, s2.TypeRegistry)
			}
		}
	}
	return report, nil
}

// ── Logs ops ─────────────────────────────────────────────────────────────────

// LogsDir returns the logs directory next to the config file.
func LogsDir(engine *WikiEngine) string {
	return filepath.Join(engine.State.StateDir, "logs")
}

// LogsTail returns the last n lines of the newest log file.
func LogsTail(engine *WikiEngine, n int) (string, error) {
	dir := LogsDir(engine)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no log directory at %s", dir)
		}
		return "", err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no log files in %s", dir)
	}
	sort.Strings(names)
	raw, err := os.ReadFile(filepath.Join(dir, names[len(names)-1]))
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if n <= 0 || n > len(lines) {
		n = len(lines)
	}
	return strings.Join(lines[len(lines)-n:], "\n"), nil
}

// LogsList returns sorted log file paths.
func LogsList(engine *WikiEngine) ([]string, error) {
	dir := LogsDir(engine)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no log directory at %s", dir)
		}
		return nil, err
	}
	var paths []string
	for _, e := range entries {
		if !e.IsDir() {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// LogsClear removes all log files, returning the count.
func LogsClear(engine *WikiEngine) (int, error) {
	paths, err := LogsList(engine)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range paths {
		if err := os.Remove(p); err == nil {
			n++
		}
	}
	return n, nil
}

var _ = assets.Schema // keep assets import for schema ops helpers

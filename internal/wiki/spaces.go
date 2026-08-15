package wiki

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"llm-wiki-go/internal/assets"
)

// SpacesCreateReport is the outcome of a wiki space creation.
type SpacesCreateReport struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	Created    bool   `json:"created"`
	Registered bool   `json:"registered"`
	Committed  bool   `json:"committed"`
}

// SpacesRegisterReport is the outcome of registering an existing wiki.
type SpacesRegisterReport struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	Registered bool   `json:"registered"`
	Created    bool   `json:"created"`
	Committed  bool   `json:"committed"`
}

// SpacesCreate creates a new wiki repository, registers it, optionally commits.
func SpacesCreate(path, name, description string, force, setAsDefault bool, configPath, wikiRootOverride string) (*SpacesCreateReport, error) {
	created := false
	if !dirExists(path) {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return nil, err
		}
		created = true
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	wikiRoot := wikiRootOverride
	if wikiRoot == "" {
		wikiRoot = "wiki"
	}
	committed := false

	global, err := LoadGlobal(configPath)
	if err != nil {
		return nil, err
	}
	for _, existing := range global.Wikis {
		if existing.Path == abs {
			if existing.Name == name {
				if err := ensureWikiStructure(abs, name, description, wikiRoot); err != nil {
					return nil, err
				}
				return &SpacesCreateReport{Path: abs, Name: name}, nil
			} else if !force {
				return nil, fmt.Errorf("wiki already registered as %q. Use --force to rename.", existing.Name)
			}
			break
		}
	}

	if err := ensureWikiStructure(abs, name, description, wikiRoot); err != nil {
		return nil, err
	}
	if !dirExists(filepath.Join(abs, ".git")) {
		if err := GitInit(abs); err != nil {
			return nil, err
		}
	}
	if hash, err := GitCommit(abs, fmt.Sprintf("create: %s", name)); err == nil && hash != "" {
		committed = true
	}

	entry := WikiEntry{Name: name, Path: abs}
	if description != "" {
		d := description
		entry.Description = &d
	}
	if err := RegisterWiki(entry, force, configPath); err != nil {
		return nil, err
	}

	if parent := filepath.Dir(configPath); parent != "" {
		logsDir := filepath.Join(parent, "logs")
		if !dirExists(logsDir) {
			os.MkdirAll(logsDir, 0o755)
		}
	}
	if setAsDefault {
		if err := SetDefaultWiki(name, configPath); err != nil {
			return nil, err
		}
	}
	return &SpacesCreateReport{Path: abs, Name: name, Created: created, Registered: true, Committed: committed}, nil
}

// SpacesRegisterExisting registers an existing wiki without creating files.
func SpacesRegisterExisting(path, name, description, wikiRootOverride, configPath string) (*SpacesRegisterReport, error) {
	if !dirExists(path) {
		return nil, fmt.Errorf("path %q does not exist", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}

	existingRoot := ""
	tomlPath := filepath.Join(abs, "wiki.toml")
	if raw, err := os.ReadFile(tomlPath); err == nil && strings.Contains(string(raw), "wiki_root") {
		if cfg, err := LoadWiki(abs); err == nil && cfg.WikiRoot != "" {
			existingRoot = cfg.WikiRoot
		}
	}
	var effectiveRoot string
	switch {
	case wikiRootOverride != "" && existingRoot != "" && wikiRootOverride != existingRoot:
		return nil, fmt.Errorf("wiki.toml already declares wiki_root = %q. Remove it manually before registering with a different value.", existingRoot)
	case wikiRootOverride != "":
		effectiveRoot = wikiRootOverride
	case existingRoot != "":
		effectiveRoot = existingRoot
	default:
		effectiveRoot = "wiki"
	}

	if err := ValidateWikiRoot(abs, effectiveRoot); err != nil {
		return nil, err
	}
	if err := ensureWikiStructure(abs, name, description, effectiveRoot); err != nil {
		return nil, err
	}

	entry := WikiEntry{Name: name, Path: abs}
	if description != "" {
		d := description
		entry.Description = &d
	}
	global, err := LoadGlobal(configPath)
	if err != nil {
		return nil, err
	}
	already := false
	for _, w := range global.Wikis {
		if w.Name == name {
			already = true
			break
		}
	}
	if !already {
		if err := RegisterWiki(entry, false, configPath); err != nil {
			return nil, err
		}
	}
	return &SpacesRegisterReport{Path: abs, Name: name, Registered: !already}, nil
}

func ensureWikiStructure(path, name, description, wikiRoot string) error {
	for _, dir := range []string{"inbox", "raw", "schemas"} {
		d := filepath.Join(path, dir)
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
		gk := filepath.Join(d, ".gitkeep")
		if !fileExists(gk) {
			os.WriteFile(gk, nil, 0o644)
		}
	}
	wikiDir := filepath.Join(path, wikiRoot)
	if err := os.MkdirAll(wikiDir, 0o755); err != nil {
		return err
	}
	if gk := filepath.Join(wikiDir, ".gitkeep"); !fileExists(gk) {
		os.WriteFile(gk, nil, 0o644)
	}

	schemasDir := filepath.Join(path, "schemas")
	for _, schemaName := range assets.SchemaNames() {
		dest := filepath.Join(schemasDir, schemaName)
		if !fileExists(dest) {
			if err := os.WriteFile(dest, assets.Schema(schemaName), 0o644); err != nil {
				return err
			}
		}
	}

	readme := filepath.Join(path, "README.md")
	if !fileExists(readme) {
		descLine := ""
		if description != "" {
			descLine = "\n" + description + "\n"
		}
		content := fmt.Sprintf("# %s\n%s\nManaged by [llm-wiki](https://github.com/geronimo-iia/llm-wiki). Run `llm-wiki serve` to start the MCP server.\n", name, descLine)
		if err := os.WriteFile(readme, []byte(content), 0o644); err != nil {
			return err
		}
	}

	wikiToml := filepath.Join(path, "wiki.toml")
	if !fileExists(wikiToml) {
		var b strings.Builder
		fmt.Fprintf(&b, "name = \"%s\"\n", name)
		if description != "" {
			fmt.Fprintf(&b, "description = \"%s\"\n", description)
		}
		if wikiRoot != "wiki" {
			fmt.Fprintf(&b, "wiki_root = \"%s\"\n", wikiRoot)
		}
		if err := os.WriteFile(wikiToml, []byte(b.String()), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// ValidateWikiRoot checks a custom content directory before registration.
func ValidateWikiRoot(repoPath, wikiRoot string) error {
	if wikiRoot == "" || wikiRoot == "." {
		return fmt.Errorf("wiki_root must not be empty or \".\"")
	}
	if filepath.IsAbs(wikiRoot) || strings.HasPrefix(wikiRoot, "/") {
		return fmt.Errorf("wiki_root must be a relative path (no leading \"/\")")
	}
	for _, seg := range strings.Split(wikiRoot, "/") {
		if seg == ".." {
			return fmt.Errorf("wiki_root must not contain \"..\" components")
		}
	}
	top := strings.Split(wikiRoot, "/")[0]
	for _, reserved := range []string{"inbox", "raw", "schemas"} {
		if top == reserved {
			return fmt.Errorf("wiki_root %q uses reserved directory %q", wikiRoot, reserved)
		}
	}
	candidate := filepath.Join(repoPath, wikiRoot)
	if !dirExists(candidate) {
		return fmt.Errorf("wiki_root directory %q does not exist", candidate)
	}
	repoAbs, err := filepath.Abs(repoPath)
	if err != nil {
		return fmt.Errorf("cannot canonicalize repo path %s: %v", repoPath, err)
	}
	rootAbs, err := filepath.Abs(candidate)
	if err != nil || !strings.HasPrefix(rootAbs, repoAbs) {
		return fmt.Errorf("wiki_root must be inside the repository (resolved to %s, repo is %s)", rootAbs, repoAbs)
	}
	return nil
}

// ResolveWikiName looks up a registered wiki by name.
func ResolveWikiName(name string, global *GlobalConfig) (WikiEntry, error) {
	for _, w := range global.Wikis {
		if w.Name == name {
			return w, nil
		}
	}
	return WikiEntry{}, fmt.Errorf("wiki %q is not registered", name)
}

// RegisterWiki adds or updates a wiki entry in the global config.
func RegisterWiki(entry WikiEntry, force bool, configPath string) error {
	config, err := LoadGlobal(configPath)
	if err != nil {
		return err
	}
	found := false
	for i, w := range config.Wikis {
		if w.Name == entry.Name {
			if !force {
				return fmt.Errorf("wiki already registered as %q. Use --force to update.", entry.Name)
			}
			config.Wikis[i] = entry
			found = true
			break
		}
	}
	if !found {
		config.Wikis = append(config.Wikis, entry)
	}
	return SaveGlobal(config, configPath)
}

// RemoveWiki unregisters a wiki; delete also removes its directory.
func RemoveWiki(name string, del bool, configPath string) error {
	config, err := LoadGlobal(configPath)
	if err != nil {
		return err
	}
	if config.Global.DefaultWiki == name {
		return fmt.Errorf("%q is the default wiki — set a new default first", name)
	}
	idx := -1
	for i, w := range config.Wikis {
		if w.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("wiki %q is not registered", name)
	}
	entry := config.Wikis[idx]
	config.Wikis = append(config.Wikis[:idx], config.Wikis[idx+1:]...)
	if del && dirExists(entry.Path) {
		if err := os.RemoveAll(entry.Path); err != nil {
			return err
		}
	}
	return SaveGlobal(config, configPath)
}

// SetDefaultWiki sets the default wiki in the global config.
func SetDefaultWiki(name, configPath string) error {
	config, err := LoadGlobal(configPath)
	if err != nil {
		return err
	}
	found := false
	for _, w := range config.Wikis {
		if w.Name == name {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("wiki %q is not registered", name)
	}
	config.Global.DefaultWiki = name
	return SaveGlobal(config, configPath)
}

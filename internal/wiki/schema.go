package wiki

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"llm-wiki-go/internal/assets"
)

// EdgeDecl is an x-graph-edges declaration in a type schema: a frontmatter
// field whose values become labeled graph edges.
type EdgeDecl struct {
	Field       string   `json:"field"`
	Relation    string   `json:"relation"`
	Direction   string   `json:"direction"`
	TargetTypes []string `json:"target_types"`
}

// RegisteredType is one page type compiled from schemas/*.json.
type RegisteredType struct {
	SchemaPath     string
	Description    string
	Aliases        map[string]string // alias -> canonical field
	RequiredFields []string
	ContentHash    string // sha256 of the schema file text
	Edges          []EdgeDecl
	schema         *jsonschema.Schema
}

// Edges returns the edge declarations for a type (empty for unknown types).
func (r *TypeRegistry) Edges(typeName string) []EdgeDecl {
	if t, ok := r.Types[typeName]; ok {
		return t.Edges
	}
	return nil
}

// TypeRegistry is the per-wiki compiled set of page types.
type TypeRegistry struct {
	Types         map[string]*RegisteredType
	GlobalHash    string // sha256 over sorted per-type hashes
	PerTypeHashes map[string]string
}

// Has reports whether a type name is registered.
func (r *TypeRegistry) Has(typeName string) bool {
	_, ok := r.Types[typeName]
	return ok
}

// Description returns the registered description, or "".
func (r *TypeRegistry) Description(typeName string) string {
	if t, ok := r.Types[typeName]; ok {
		return t.Description
	}
	return ""
}

// FieldKind classifies an index field.
type FieldKind int

// Index field kinds.
const (
	FieldText FieldKind = iota
	FieldKeyword
	FieldNumeric
)

// IndexSchema is the union index field set derived from all type schemas.
type IndexSchema struct {
	Fields map[string]FieldKind
	// Aliases maps alias field -> canonical field across all types.
	Aliases map[string]string
	// EdgeFields is the union of x-graph-edges keys.
	EdgeFields map[string]bool
}

// HasField reports whether name is an index field.
func (s *IndexSchema) HasField(name string) bool {
	_, ok := s.Fields[name]
	return ok
}

// Kind returns the field kind, or FieldText for unknown fields.
func (s *IndexSchema) Kind(name string) FieldKind {
	if k, ok := s.Fields[name]; ok {
		return k
	}
	return FieldText
}

// schemaFile is the parsed shape of a schemas/*.json file.
type schemaFile struct {
	path    string
	raw     string
	props   map[string]map[string]any // property name -> property schema
	types   map[string]string         // x-wiki-types: name -> description
	aliases map[string]string
	edges   map[string]EdgeDecl
}

// BuildSpace compiles schemas/*.json (plus wiki.toml [types] overrides,
// plus embedded defaults when absent) into a TypeRegistry and IndexSchema.
func BuildSpace(repoRoot string) (*TypeRegistry, *IndexSchema, error) {
	schemasDir := filepath.Join(repoRoot, "schemas")
	var files []*schemaFile

	entries, err := os.ReadDir(schemasDir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			sf, err := parseSchemaFile(filepath.Join(schemasDir, e.Name()))
			if err != nil {
				return nil, nil, fmt.Errorf("invalid schema %s: %w", e.Name(), err)
			}
			files = append(files, sf)
		}
	} else if !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("failed to read %s: %w", schemasDir, err)
	}

	// wiki.toml explicit type registrations (override by schema path)
	wikiCfg, err := LoadWiki(repoRoot)
	if err != nil {
		return nil, nil, err
	}
	type overrideEntry struct{ name, path string }
	var overrides []overrideEntry
	for name, te := range wikiCfg.Types {
		overrides = append(overrides, overrideEntry{name, filepath.Join(repoRoot, te.Schema)})
	}
	sort.Slice(overrides, func(i, j int) bool { return overrides[i].name < overrides[j].name })
	for _, ov := range overrides {
		if !containsPath(files, ov.path) {
			sf, err := parseSchemaFile(ov.path)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid schema %s: %w", ov.path, err)
			}
			files = append(files, sf)
		}
	}

	// no schemas at all → embedded defaults
	if len(files) == 0 {
		for _, name := range assets.SchemaNames() {
			if !strings.HasSuffix(name, ".json") {
				continue
			}
			raw := assets.Schema(name)
			sf, err := parseSchemaRaw(name, string(raw))
			if err != nil {
				return nil, nil, fmt.Errorf("invalid embedded schema %s: %w", name, err)
			}
			files = append(files, sf)
		}
	}

	registry := &TypeRegistry{Types: map[string]*RegisteredType{}, PerTypeHashes: map[string]string{}}
	indexSchema := &IndexSchema{
		Fields: map[string]FieldKind{
			"slug": FieldKeyword, "uri": FieldKeyword,
			"body": FieldText, "body_links": FieldKeyword,
		},
		Aliases:    map[string]string{},
		EdgeFields: map[string]bool{},
	}

	// sorted-by-filename iteration keeps first-seen field wins deterministic
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })

	seenField := map[string]bool{"slug": true, "uri": true, "body": true, "body_links": true}

	for _, sf := range files {
		compiler := jsonschema.NewCompiler()
		var doc any
		if err := json.Unmarshal([]byte(sf.raw), &doc); err != nil {
			return nil, nil, fmt.Errorf("invalid JSON in %s: %w", sf.path, err)
		}
		url := "file://" + filepath.ToSlash(sf.path)
		if err := compiler.AddResource(url, doc); err != nil {
			return nil, nil, fmt.Errorf("invalid schema %s: %w", sf.path, err)
		}
		compiled, err := compiler.Compile(url)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid schema %s: %w", sf.path, err)
		}

		var required []string
		if req, ok := doc.(map[string]any)["required"].([]any); ok {
			for _, r := range req {
				if s, ok := r.(string); ok {
					required = append(required, s)
				}
			}
		}

		// index schema classification: aliased fields index under canonical name
		for name, prop := range sf.props {
			if _, aliased := sf.aliases[name]; aliased {
				continue // alias sources index under their canonical name
			}
			if !seenField[name] {
				seenField[name] = true
				indexSchema.Fields[name] = classifyField(prop, hasKey(sf.edges, name))
			}
		}
		for alias, canonical := range sf.aliases {
			indexSchema.Aliases[alias] = canonical
		}
		for field := range sf.edges {
			indexSchema.EdgeFields[field] = true
			if !seenField[field] {
				seenField[field] = true
				indexSchema.Fields[field] = FieldKeyword
			}
		}

		for typeName, desc := range sf.types {
			if desc == "" {
				if wikiCfg.Types[typeName].Description != "" {
					desc = wikiCfg.Types[typeName].Description
				}
			}
			rt := &RegisteredType{
				SchemaPath:     sf.path,
				Description:    desc,
				Aliases:        sf.aliases,
				RequiredFields: required,
				ContentHash:    hashText(sf.raw),
				Edges:          edgesSorted(sf.edges),
				schema:         compiled,
			}
			registry.Types[typeName] = rt
			registry.PerTypeHashes[typeName] = typeHash(rt)
		}
	}

	// base invariant: ensure a "default" type exists
	if _, ok := registry.Types["default"]; !ok {
		raw := assets.Schema("base.json")
		sf, err := parseSchemaRaw("base.json (embedded)", string(raw))
		if err != nil {
			return nil, nil, err
		}
		compiler := jsonschema.NewCompiler()
		var doc any
		_ = json.Unmarshal([]byte(sf.raw), &doc)
		url := "https://github.com/geronimo-iia/llm-wiki/schemas/v0.1.0/base.json"
		if err := compiler.AddResource(url, doc); err != nil {
			return nil, nil, err
		}
		compiled, err := compiler.Compile(url)
		if err != nil {
			return nil, nil, err
		}
		registry.Types["default"] = &RegisteredType{
			SchemaPath:     "base.json (embedded)",
			Description:    "Fallback for unrecognized types",
			RequiredFields: []string{"title", "type"},
			ContentHash:    hashText(sf.raw),
			schema:         compiled,
		}
		registry.PerTypeHashes["default"] = hashText(sf.raw)
	} else {
		rt := registry.Types["default"]
		hasTitle, hasType := false, false
		for _, r := range rt.RequiredFields {
			if r == "title" {
				hasTitle = true
			}
			if r == "type" {
				hasType = true
			}
		}
		if !hasTitle || !hasType {
			return nil, nil, fmt.Errorf("base schema '%s' must require 'title'/'type' — the default type is the fallback for all unknown types", rt.SchemaPath)
		}
	}

	registry.GlobalHash = globalHash(registry.PerTypeHashes)
	return registry, indexSchema, nil
}

// ValidateType validates frontmatter against its type schema.
// strictness: "strict" errors on unknown types and schema violations;
// "loose" downgrades them to warnings. Returns (warnings, error).
func (r *TypeRegistry) ValidateType(fm Frontmatter, strictness string) ([]string, error) {
	var warnings []string

	title, _ := fm["title"].(string)
	name, _ := fm["name"].(string)
	if title == "" && name == "" {
		return nil, fmt.Errorf("title is required")
	}

	typeName, _ := fm["type"].(string)
	if typeName == "" {
		warnings = append(warnings, "missing field: type (defaulting to \"page\")")
		typeName = "default"
	}
	rt, ok := r.Types[typeName]
	if !ok {
		if strictness == "strict" {
			return nil, fmt.Errorf("unknown type '%s'", typeName)
		}
		warnings = append(warnings, fmt.Sprintf("unknown type '%s'", typeName))
		rt = r.Types["default"]
	}
	if rt == nil || rt.schema == nil {
		return warnings, nil
	}

	inst := yamlToJSONValue(fm)
	if err := rt.schema.Validate(inst); err != nil {
		if strictness == "strict" {
			return nil, fmt.Errorf("schema validation failed: %v", firstSchemaError(err))
		}
		warnings = append(warnings, fmt.Sprintf("schema validation: %v", firstSchemaError(err)))
	}
	return warnings, nil
}

func firstSchemaError(err error) string {
	var out string
	for _, line := range strings.Split(err.Error(), "\n") {
		if out == "" {
			out = line
		} else {
			out += "; " + line
		}
		if len(out) > 300 {
			break
		}
	}
	return out
}

// yamlToJSONValue normalizes YAML-decoded values into JSON-compatible
// shapes (map[string]any keys, float64 numbers).
func yamlToJSONValue(v any) any {
	switch t := v.(type) {
	case Frontmatter:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = yamlToJSONValue(vv)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = yamlToJSONValue(vv)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[fmt.Sprint(k)] = yamlToJSONValue(vv)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = yamlToJSONValue(vv)
		}
		return out
	case int:
		return float64(t)
	case int64:
		return float64(t)
	default:
		return v
	}
}

func parseSchemaFile(path string) (*schemaFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseSchemaRaw(path, string(raw))
}

func parseSchemaRaw(path, raw string) (*schemaFile, error) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return nil, err
	}
	sf := &schemaFile{path: path, raw: raw, types: map[string]string{}}
	if props, ok := doc["properties"].(map[string]any); ok {
		sf.props = map[string]map[string]any{}
		for name, p := range props {
			if pm, ok := p.(map[string]any); ok {
				sf.props[name] = pm
			}
		}
	}
	if wt, ok := doc["x-wiki-types"].(map[string]any); ok {
		for name, d := range wt {
			if s, ok := d.(string); ok {
				sf.types[name] = s
			} else {
				sf.types[name] = ""
			}
		}
	}
	if al, ok := doc["x-index-aliases"].(map[string]any); ok {
		sf.aliases = map[string]string{}
		for from, to := range al {
			if s, ok := to.(string); ok {
				sf.aliases[from] = s
			}
		}
	}
	if ge, ok := doc["x-graph-edges"].(map[string]any); ok {
		sf.edges = map[string]EdgeDecl{}
		for field, spec := range ge {
			decl := EdgeDecl{Field: field, Relation: "links-to", Direction: "outgoing"}
			if sm, ok := spec.(map[string]any); ok {
				if r, ok := sm["relation"].(string); ok && r != "" {
					decl.Relation = r
				}
				if d, ok := sm["direction"].(string); ok && d != "" {
					decl.Direction = d
				}
				if tts, ok := sm["target_types"].([]any); ok {
					for _, tt := range tts {
						if s, ok := tt.(string); ok {
							decl.TargetTypes = append(decl.TargetTypes, s)
						}
					}
				}
			}
			sf.edges[field] = decl
		}
	}
	return sf, nil
}

// classifyField ports the Rust field classification.
func classifyField(prop map[string]any, isEdgeField bool) FieldKind {
	if isEdgeField {
		return FieldKeyword
	}
	t, _ := prop["type"].(string)
	switch t {
	case "string":
		if hasEnumOrConst(prop) {
			return FieldKeyword
		}
		return FieldText
	case "boolean":
		return FieldKeyword
	case "array":
		if b, _ := prop["x-keyword"].(bool); b {
			return FieldKeyword
		}
		if items, ok := prop["items"].(map[string]any); ok && hasEnumOrConst(items) {
			return FieldKeyword
		}
		return FieldText
	case "number", "integer":
		return FieldNumeric
	default:
		return FieldText
	}
}

func hasEnumOrConst(prop map[string]any) bool {
	if _, ok := prop["enum"]; ok {
		return true
	}
	_, ok := prop["const"]
	return ok
}

func hasKey[K comparable, V any](m map[K]V, k K) bool {
	_, ok := m[k]
	return ok
}

func aliasFor(aliases map[string]string, canonical string) (string, bool) {
	for a, c := range aliases {
		if c == canonical {
			return a, true
		}
	}
	return "", false
}

func edgesSorted(m map[string]EdgeDecl) []EdgeDecl {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]EdgeDecl, 0, len(keys))
	for _, k := range keys {
		out = append(out, m[k])
	}
	return out
}

func containsPath(files []*schemaFile, path string) bool {
	abs1, err1 := filepath.Abs(path)
	for _, f := range files {
		abs2, err2 := filepath.Abs(f.path)
		if err1 == nil && err2 == nil && abs1 == abs2 {
			return true
		}
		if f.path == path {
			return true
		}
	}
	return false
}

func hashText(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func typeHash(rt *RegisteredType) string {
	var b strings.Builder
	b.WriteString(rt.SchemaPath)
	keys := make([]string, 0, len(rt.Aliases))
	for k := range rt.Aliases {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(rt.Aliases[k])
	}
	b.WriteString(rt.ContentHash)
	return hashText(b.String())
}

func globalHash(perType map[string]string) string {
	names := make([]string, 0, len(perType))
	for n := range perType {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n)
		b.WriteString(perType[n])
	}
	return hashText(b.String())
}

// ComputeDiskHashes recomputes (global, per-type) hashes from schemas on
// disk without building validators — used for staleness checks.
func ComputeDiskHashes(repoRoot string) (string, map[string]string, error) {
	schemasDir := filepath.Join(repoRoot, "schemas")
	perType := map[string]string{}
	entries, err := os.ReadDir(schemasDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", perType, nil
		}
		return "", nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(schemasDir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", nil, err
		}
		sf, err := parseSchemaRaw(path, string(raw))
		if err != nil {
			continue
		}
		contentHash := hashText(string(raw))
		for typeName := range sf.types {
			var b strings.Builder
			b.WriteString(path)
			for _, k := range sortedKeys(sf.aliases) {
				b.WriteString(k)
				b.WriteString("=")
				b.WriteString(sf.aliases[k])
			}
			b.WriteString(contentHash)
			perType[typeName] = hashText(b.String())
		}
	}
	return globalHash(perType), perType, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

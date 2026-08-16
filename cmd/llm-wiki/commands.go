package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"llm-wiki-go/internal/wiki"
)

func (c *cli) cmdSpaces(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: spaces <create|register|list|remove|set-default> ...")
		return 2
	}
	sub, flags := parseFlags(args[1:])
	switch args[0] {
	case "create":
		if len(sub) < 1 || flags["name"] == "" {
			fmt.Fprintln(os.Stderr, "usage: spaces create <path> --name <N>")
			return 2
		}
		report, err := wiki.SpacesCreate(sub[0], flags["name"], flags["description"], flagBool(flags, "force"), flagBool(flags, "set-default"), c.configArg, flags["wiki-root"])
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if report.Created {
			fmt.Printf("Created wiki %q at %s\n", report.Name, report.Path)
		} else {
			fmt.Printf("Wiki %q at %s already exists\n", report.Name, report.Path)
		}
		if report.Registered {
			fmt.Printf("Registered in %s\n", c.configArg)
		}
		if report.Committed {
			fmt.Printf("Initial commit: create: %s\n", report.Name)
		}
		return 0
	case "register":
		if len(sub) < 1 || flags["name"] == "" {
			fmt.Fprintln(os.Stderr, "usage: spaces register <path> --name <N>")
			return 2
		}
		report, err := wiki.SpacesRegisterExisting(sub[0], flags["name"], flags["description"], flags["wiki-root"], c.configArg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Printf("Registered wiki %q at %s\n", report.Name, report.Path)
		if report.Registered {
			fmt.Printf("Registered in %s\n", c.configArg)
		} else {
			fmt.Printf("Wiki %q already registered\n", report.Name)
		}
		return 0
	case "list":
		global, err := wiki.LoadGlobal(c.configArg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		name := ""
		if len(sub) > 0 {
			name = sub[0]
		}
		if flags["format"] == "json" {
			var out []map[string]any
			for _, w := range global.Wikis {
				if name != "" && w.Name != name {
					continue
				}
				m := map[string]any{"name": w.Name, "path": w.Path}
				if w.Description != nil {
					m["description"] = *w.Description
				}
				out = append(out, m)
			}
			return printJSONOr(out, 1)
		}
		if len(global.Wikis) == 0 {
			fmt.Println("No wikis registered.")
			return 0
		}
		fmt.Printf("  %-12s %-40s description\n", "NAME", "PATH")
		for _, w := range global.Wikis {
			if name != "" && w.Name != name {
				continue
			}
			marker := " "
			if global.Global.DefaultWiki == w.Name {
				marker = "*"
			}
			desc := "—"
			if w.Description != nil && *w.Description != "" {
				desc = *w.Description
			}
			fmt.Printf("%s %-12s %-40s %s\n", marker, w.Name, w.Path, desc)
		}
		return 0
	case "remove":
		if len(sub) < 1 {
			fmt.Fprintln(os.Stderr, "usage: spaces remove <name> [--delete]")
			return 2
		}
		if err := wiki.RemoveWiki(sub[0], flagBool(flags, "delete"), c.configArg); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Printf("Removed wiki %q\n", sub[0])
		if flagBool(flags, "delete") {
			fmt.Println("Deleted wiki directory")
		}
		return 0
	case "set-default":
		if len(sub) < 1 {
			fmt.Fprintln(os.Stderr, "usage: spaces set-default <name>")
			return 2
		}
		if err := wiki.SetDefaultWiki(sub[0], c.configArg); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Printf("Default wiki set to %q\n", sub[0])
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown spaces subcommand: %s\n", args[0])
		return 2
	}
}

func printJSONOr(v any, failCode int) int {
	if err := printJSON(v); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return failCode
	}
	return 0
}

func (c *cli) cmdConfig(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: config <get|set|list> ...")
		return 2
	}
	sub, flags := parseFlags(args[1:])
	switch args[0] {
	case "get":
		if len(sub) < 1 {
			fmt.Fprintln(os.Stderr, "usage: config get <key>")
			return 2
		}
		val, err := wiki.OpsConfigGet(c.engineOrDie(), c.wikiFlag, sub[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Println(val)
		return 0
	case "set":
		if len(sub) < 2 {
			fmt.Fprintln(os.Stderr, "usage: config set <key> <value> [--global]")
			return 2
		}
		msg, err := wiki.OpsConfigSet(c.engineOrDie(), c.wikiFlag, sub[0], sub[1], flagBool(flags, "global"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Println(msg)
		return 0
	case "list":
		e := c.engineOrDie()
		if flagBool(flags, "global") {
			text, err := wiki.OpsConfigListGlobal(e)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				return 1
			}
			fmt.Print(text)
			return 0
		}
		space, err := e.ResolveWiki(c.wikiFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		return printJSONOr(space.Resolved, 1)
	default:
		fmt.Fprintf(os.Stderr, "unknown config subcommand: %s\n", args[0])
		return 2
	}
}

func (c *cli) cmdContent(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: content <read|write|new|commit> ...")
		return 2
	}
	sub, flags := parseFlags(args[1:])
	switch args[0] {
	case "read":
		if len(sub) < 1 {
			fmt.Fprintln(os.Stderr, "usage: content read <uri> [--no-frontmatter] [--list-assets]")
			return 2
		}
		result, err := wiki.ContentRead(c.engineOrDie(), sub[0], c.wikiFlag, flagBool(flags, "no-frontmatter"), flagBool(flags, "list-assets"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		switch result.Kind {
		case wiki.ContentAssets:
			for _, a := range result.Assets {
				fmt.Println(a)
			}
			return 0
		case wiki.ContentBinary:
			fmt.Fprintln(os.Stderr, "error: asset is binary — access it directly from the filesystem")
			return 1
		default:
			fmt.Print(result.Content)
			return 0
		}
	case "write":
		if len(sub) < 1 {
			fmt.Fprintln(os.Stderr, "usage: content write <uri> [--file <PATH>]")
			return 2
		}
		var content []byte
		var err error
		if file, ok := flags["file"]; ok && file != "" {
			content, err = os.ReadFile(file)
		} else {
			content, err = io.ReadAll(os.Stdin)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		res, _, err := wiki.ContentWrite(c.engineOrDie(), sub[0], string(content), c.wikiFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Printf("Wrote %d bytes to %s\n", res.BytesWritten, res.Path)
		return 0
	case "new":
		if len(sub) < 1 {
			fmt.Fprintln(os.Stderr, "usage: content new <uri> [--section] [--bundle] [--name] [--type] [--dry-run]")
			return 2
		}
		section := flagBool(flags, "section")
		bundle := flagBool(flags, "bundle")
		if flagBool(flags, "dry-run") {
			e := c.engineOrDie()
			space, err := e.ResolveWiki(c.wikiFlag)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				return 1
			}
			targetSlug := sub[0]
			if uri, err := wiki.ParseWikiUri(sub[0]); err == nil {
				targetSlug = uri.Slug.String()
				if uri.Wiki != "" {
					if _, err := e.Space(uri.Wiki); err != nil {
						targetSlug = uri.Wiki + "/" + uri.Slug.String()
					}
				}
			}
			kind := "flat"
			if section {
				kind = "section"
			} else if bundle {
				kind = "bundle"
			}
			fmt.Printf("Would create %s at wiki://%s/%s\n", kind, space.Name, targetSlug)
			return 0
		}
		result, err := wiki.ContentNew(c.engineOrDie(), sub[0], c.wikiFlag, section, bundle, flags["name"], flags["type"])
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Printf("Created: %s\n", result.URI)
		return 0
	case "commit":
		all := flagBool(flags, "all")
		var slugs []string
		slugs = append(slugs, sub...)
		hash, err := wiki.ContentCommit(c.engineOrDie(), c.wikiFlag, slugs, all, flags["m"])
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if hash == "" {
			fmt.Println("Nothing to commit")
		} else {
			fmt.Println(hash)
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown content subcommand: %s\n", args[0])
		return 2
	}
}

func (c *cli) cmdSearch(args []string) int {
	sub, flags := parseFlags(args)
	query := strings.Join(sub, " ")
	if query == "" {
		fmt.Fprintln(os.Stderr, "usage: search <query> [--type] [--no-excerpt] [--top-k] [--format]")
		return 2
	}
	mode := "keyword"
	if flagBool(flags, "semantic") {
		mode = "semantic"
	} else if flagBool(flags, "hybrid") {
		mode = "hybrid"
	}
	result, err := wiki.OpsSearch(c.engineOrDie(), c.wikiName(), wiki.SearchParams{
		Query:           query,
		TypeFilter:      flags["type"],
		NoExcerpt:       flagBool(flags, "no-excerpt"),
		TopK:            flagInt(flags, "top-k", 0),
		IncludeSections: flagBool(flags, "include-sections"),
		CrossWiki:       flagBool(flags, "cross-wiki"),
		Mode:            mode,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	switch flags["format"] {
	case "json":
		return printJSONOr(result, 1)
	case "llms":
		fmt.Print(wiki.RenderSearchLLMS(result))
		return 0
	default:
		for _, r := range result.Results {
			fmt.Printf("slug:  %s\n", r.Slug)
			fmt.Printf("uri:   %s\n", r.URI)
			fmt.Printf("title: %s\n", r.Title)
			fmt.Printf("score: %.2f\n", r.Score)
			if r.Excerpt != nil {
				fmt.Printf("excerpt: %s\n", *r.Excerpt)
			}
			fmt.Println()
		}
		return 0
	}
}

func (c *cli) cmdList(args []string) int {
	_, flags := parseFlags(args)
	result, _, err := wiki.OpsList(c.engineOrDie(), c.wikiName(), flags["type"], flags["status"], flagInt(flags, "page", 1), flagInt(flags, "page-size", 0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	switch flags["format"] {
	case "json":
		return printJSONOr(result, 1)
	case "llms":
		fmt.Print(wiki.RenderListLLMS(result))
		return 0
	default:
		for _, p := range result.Pages {
			fmt.Printf("%-40s %-16s %-8s %s\n", p.Slug, p.Type, p.Status, p.Title)
		}
		totalPages := 1
		if result.PageSize > 0 {
			totalPages = (result.Total + result.PageSize - 1) / result.PageSize
		}
		fmt.Printf("\nPage %d/%d (%d total)\n", result.Page, totalPages, result.Total)
		return 0
	}
}

func (c *cli) cmdIngest(args []string) int {
	sub, flags := parseFlags(args)
	path := "."
	if len(sub) > 0 {
		path = sub[0]
	}
	report, _, err := wiki.OpsIngest(c.engineOrDie(), c.wikiName(), path, flagBool(flags, "dry-run"), flagBool(flags, "redact"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if flags["format"] == "json" {
		return printJSONOr(report, 1)
	}
	redactions := 0
	for _, r := range report.Redacted {
		redactions += len(r.Matches)
	}
	fmt.Printf("Ingested: %d pages, %d unchanged, %d assets, %d warnings, %d redactions\n",
		report.PagesValidated, report.UnchangedCount, report.AssetsFound, len(report.Warnings), redactions)
	for _, w := range report.Warnings {
		fmt.Printf("  warn: %s\n", w)
	}
	for _, r := range report.Redacted {
		for _, m := range r.Matches {
			fmt.Printf("  redacted: %s line %d [%s]\n", r.Slug, m.LineNumber, m.PatternName)
		}
	}
	if flagBool(flags, "dry-run") {
		fmt.Println("(dry run — nothing committed)")
	} else if report.Commit != "" {
		fmt.Printf("Commit: %s\n", report.Commit)
	}
	return 0
}

func (c *cli) cmdGraph(args []string) int {
	_, flags := parseFlags(args)
	depth := 0
	hasDepth := false
	if v, ok := flags["depth"]; ok {
		if n, err := fmt.Sscanf(v, "%d", &depth); n == 1 && err == nil {
			hasDepth = true
		}
	}
	result, err := wiki.OpsGraphBuild(c.engineOrDie(), c.wikiName(), wiki.GraphParams{
		Format:     flags["format"],
		Root:       flags["root"],
		Depth:      depth,
		HasDepth:   hasDepth,
		TypeFilter: flags["type"],
		Relation:   flags["relation"],
		Output:     flags["output"],
		CrossWiki:  flagBool(flags, "cross-wiki"),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if flags["output"] != "" && flags["output"] != "true" {
		fmt.Printf("Wrote graph to %s\n", result.Report.Output)
		return 0
	}
	fmt.Print(result.Rendered)
	return 0
}

func (c *cli) cmdIndex(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: index <rebuild|status>")
		return 2
	}
	_, flags := parseFlags(args[1:])
	switch args[0] {
	case "rebuild":
		name := c.wikiName()
		space, err := c.engineOrDie().Space(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if flagBool(flags, "dry-run") {
			count := 0
			filepathWalkMd(space.WikiRoot, func() { count++ })
			fmt.Printf("Would index %d pages from %s\n", count, space.WikiRoot)
			return 0
		}
		report, err := wiki.OpsIndexRebuild(c.engine, name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if flags["format"] == "json" {
			return printJSONOr(report, 1)
		}
		fmt.Printf("Indexed %d pages in %dms\n", report.PagesIndexed, report.DurationMs)
		return 0
	case "status":
		status, space, err := wiki.OpsIndexStatus(c.engineOrDie(), c.wikiName())
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if flags["format"] == "json" {
			return printJSONOr(status, 1)
		}
		built := "never"
		if status.Built != nil {
			built = *status.Built
		}
		stale := "no"
		if status.Stale {
			stale = "yes"
		}
		openable := "yes"
		if !status.Openable {
			openable = "no"
		}
		queryable := "yes"
		if !status.Queryable {
			queryable = "no"
		}
		fmt.Printf("wiki:    %s\n", space.Name)
		fmt.Printf("path:    %s\n", space.IndexManager.IndexPath)
		fmt.Printf("built:   %s\n", built)
		fmt.Printf("pages:   %d\n", status.Pages)
		fmt.Printf("sections:%d\n", status.Sections)
		fmt.Printf("stale:   %s\n", stale)
		fmt.Printf("openable:%s\n", openable)
		fmt.Printf("queryable:%s\n", queryable)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown index subcommand: %s\n", args[0])
		return 2
	}
}

func (c *cli) cmdHistory(args []string) int {
	sub, flags := parseFlags(args)
	if len(sub) < 1 {
		fmt.Fprintln(os.Stderr, "usage: history <slug> [-n <limit>] [--no-follow] [--format]")
		return 2
	}
	follow := !flagBool(flags, "no-follow")
	result, err := wiki.OpsHistory(c.engineOrDie(), c.wikiName(), sub[0], flagInt(flags, "n", 0), &follow)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if flags["format"] == "json" {
		return printJSONOr(result, 1)
	}
	for _, e := range result.Entries {
		hash := e.Hash
		if len(hash) > 7 {
			hash = hash[:7]
		}
		date := e.Date
		if len(date) > 10 {
			date = date[:10]
		}
		msg := e.Message
		if len(msg) > 40 {
			msg = msg[:40]
		}
		fmt.Printf("%s  %-10s  %-40s  %s\n", hash, date, msg, e.Author)
	}
	return 0
}

func (c *cli) cmdStats(args []string) int {
	_, flags := parseFlags(args)
	stats, err := wiki.OpsStats(c.engineOrDie(), c.wikiName())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if flags["format"] == "json" {
		return printJSONOr(stats, 1)
	}
	fmt.Printf("%s — %d pages, %d sections\n", stats.Wiki, stats.Pages, stats.Sections)
	fmt.Printf("types:     %s\n", formatCounts(stats.Types))
	fmt.Printf("status:    %s\n", formatCounts(stats.Status))
	fmt.Printf("orphans:   %d\n", stats.Orphans)
	fmt.Printf("graph:     %.1f avg connections, %.2f density\n", stats.AvgConnections, stats.GraphDensity)
	fmt.Printf("staleness: fresh(%d) 7d(%d) 30d(%d)\n", stats.Staleness.Fresh, stats.Staleness.Stale7d, stats.Staleness.Stale30d)
	built := "never"
	if stats.Index.Built != nil {
		built = *stats.Index.Built
	}
	indexState := "ok"
	if stats.Index.Stale {
		indexState = "stale"
	}
	fmt.Printf("index:     %s, built %s\n", indexState, built)
	return 0
}

func formatCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s(%d)", k, m[k]))
	}
	return strings.Join(parts, " ")
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func (c *cli) cmdLint(args []string) int {
	_, flags := parseFlags(args)
	report, err := wiki.OpsLint(c.engineOrDie(), c.wikiName(), flags["rules"], flags["severity"])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if flags["format"] == "json" {
		return printJSONOr(report, 1)
	}
	if len(report.Findings) == 0 {
		fmt.Printf("wiki %s: ok (no findings)\n", report.Wiki)
		return 0
	}
	for _, f := range report.Findings {
		fmt.Printf("[%s] %s — %s (%s)\n", f.Severity, f.Slug, f.Message, f.Rule)
	}
	fmt.Printf("\n%d finding(s): %d error(s), %d warning(s)\n", report.Total, report.Errors, report.Warnings)
	if report.Errors > 0 {
		return 1
	}
	return 0
}

func (c *cli) cmdSuggest(args []string) int {
	sub, flags := parseFlags(args)
	if len(sub) < 1 {
		fmt.Fprintln(os.Stderr, "usage: suggest <slug> [-n <limit>] [--format]")
		return 2
	}
	result, err := wiki.OpsSuggest(c.engineOrDie(), c.wikiName(), sub[0], flagInt(flags, "n", 0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if flags["format"] == "json" {
		return printJSONOr(result, 1)
	}
	if len(result) == 0 {
		fmt.Println("No suggestions.")
		return 0
	}
	for _, s := range result {
		fmt.Printf("%-40s %.2f  %s\n", s.Slug, s.Score, s.Title)
		fmt.Printf("  → %s  (%s)\n", s.Field, s.Reason)
	}
	return 0
}

func (c *cli) cmdSchema(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: schema <list|show|add|remove|validate> ...")
		return 2
	}
	sub, flags := parseFlags(args[1:])
	e := c.engineOrDie()
	switch args[0] {
	case "list":
		space, err := e.Space(c.wikiName())
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		entries := wiki.OpsSchemaList(space)
		if flags["format"] == "json" {
			return printJSONOr(entries, 1)
		}
		for _, entry := range entries {
			fmt.Printf("%-16s %s\n", entry.Name, entry.Description)
		}
		return 0
	case "show":
		if len(sub) < 1 {
			fmt.Fprintln(os.Stderr, "usage: schema show <type> [--template]")
			return 2
		}
		space, err := e.Space(c.wikiName())
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		var text string
		if flagBool(flags, "template") {
			text, err = wiki.OpsSchemaShowTemplate(space, sub[0])
		} else {
			text, err = wiki.OpsSchemaShow(space, sub[0])
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Print(text)
		return 0
	case "add":
		if len(sub) < 2 {
			fmt.Fprintln(os.Stderr, "usage: schema add <type> <schema-path>")
			return 2
		}
		msg, err := wiki.OpsSchemaAdd(e, c.wikiName(), sub[0], sub[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Println(msg)
		return 0
	case "remove":
		if len(sub) < 1 {
			fmt.Fprintln(os.Stderr, "usage: schema remove <type> [--delete] [--delete-pages] [--dry-run]")
			return 2
		}
		report, err := wiki.OpsSchemaRemove(e, c.wikiName(), sub[0], flagBool(flags, "delete"), flagBool(flags, "delete-pages"), flagBool(flags, "dry-run"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if flagBool(flags, "dry-run") {
			fmt.Println("DRY RUN:")
		}
		fmt.Printf("pages removed from index: %d\n", report.PagesRemoved)
		fmt.Printf("page files deleted from disk: %d\n", report.PagesDeletedOnDisk)
		fmt.Printf("wiki.toml updated: %v\n", report.WikiTomlUpdated)
		fmt.Printf("schema file deleted: %v\n", report.SchemaFileDeleted)
		return 0
	case "validate":
		space, err := e.Space(c.wikiName())
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		issues := wiki.OpsSchemaValidate(space)
		if len(sub) > 0 && !space.TypeRegistry.Has(sub[0]) {
			issues = append(issues, fmt.Sprintf("type '%s' is not registered", sub[0]))
		}
		if len(issues) == 0 {
			fmt.Println("ok")
			return 0
		}
		for _, i := range issues {
			fmt.Println(i)
		}
		return 1
	default:
		fmt.Fprintf(os.Stderr, "unknown schema subcommand: %s\n", args[0])
		return 2
	}
}

func (c *cli) cmdExport(args []string) int {
	_, flags := parseFlags(args)
	report, err := wiki.OpsExport(c.engineOrDie(), c.wikiName(), flags["path"], wiki.ParseExportFormat(flags["format"]), flags["status"] == "all")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("Exported %d pages (%d bytes) → %s\n", report.Pages, report.Bytes, report.Path)
	return 0
}

func (c *cli) cmdResolve(args []string) int {
	sub, _ := parseFlags(args)
	if len(sub) < 1 {
		fmt.Fprintln(os.Stderr, "usage: resolve <uri>")
		return 2
	}
	result, err := wiki.ResolveUriToPath(c.engineOrDie(), sub[0], c.wikiFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return printJSONOr(result, 1)
}

func (c *cli) cmdLogs(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: logs <tail|list|clear>")
		return 2
	}
	e := c.engineOrDie()
	_, flags := parseFlags(args[1:])
	switch args[0] {
	case "tail":
		text, err := wiki.LogsTail(e, flagInt(flags, "lines", 50))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Print(text)
		return 0
	case "list":
		paths, err := wiki.LogsList(e)
		if err != nil || len(paths) == 0 {
			fmt.Println("no log files")
			return 0
		}
		for _, p := range paths {
			fmt.Println(p)
		}
		return 0
	case "clear":
		n, err := wiki.LogsClear(e)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Printf("removed %d log file(s)\n", n)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown logs subcommand: %s\n", args[0])
		return 2
	}
}

func (c *cli) cmdInfo(args []string) int {
	e := c.engineOrDie()
	type info struct {
		Version     string   `json:"version"`
		ConfigPath  string   `json:"config_path"`
		Spaces      []string `json:"spaces"`
		DefaultWiki string   `json:"default_wiki"`
	}
	payload := info{Version: "0.1.0", ConfigPath: e.State.ConfigPath, DefaultWiki: e.State.Config.Global.DefaultWiki}
	for _, s := range e.SpacesList() {
		payload.Spaces = append(payload.Spaces, s.Name)
	}
	_, flags := parseFlags(args)
	if flags["format"] == "json" {
		return printJSONOr(payload, 1)
	}
	fmt.Printf("version:      %s\n", payload.Version)
	fmt.Printf("config:       %s\n", payload.ConfigPath)
	fmt.Printf("spaces:       %s\n", strings.Join(payload.Spaces, ", "))
	fmt.Printf("default wiki: %s\n", payload.DefaultWiki)
	return 0
}

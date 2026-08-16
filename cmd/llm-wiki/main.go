// llm-wiki is a git-backed headless wiki engine for agents: typed
// Markdown pages, BM25 full-text search with Chinese tokenization, a
// concept graph, and MCP / ACP transports. Go port of
// github.com/geronimo-iia/llm-wiki.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"llm-wiki-go/internal/wiki"
)

const usageText = `llm-wiki — git-backed headless wiki engine for agents

Usage: llm-wiki [global flags] <command> [args]

Global flags:
  --wiki <NAME>    Target wiki space (default: configured default wiki)
  --config <PATH>  Config file (default: $LLM_WIKI_CONFIG or ~/.llm-wiki/config.toml)

Commands:
  spaces create <path> --name <N> [--description] [--force] [--set-default] [--wiki-root]
  spaces register <path> --name <N> [--description] [--wiki-root]
  spaces list [name] [--format json]
  spaces remove <name> [--delete]
  spaces set-default <name>
  config get <key>
  config set <key> <value> [--global]
  config list [--global] [--format json]
  content read <uri> [--no-frontmatter] [--list-assets]
  content write <uri> [--file <PATH>]   (content from file or stdin)
  content new <uri> [--section] [--bundle] [--name] [--type] [--dry-run]
  content commit [slugs…] [--all] [-m <msg>]
  search <query> [--type] [--no-excerpt] [--top-k] [--include-sections] [--cross-wiki] [--semantic|--hybrid] [--format]
  list [--type] [--status] [--page] [--page-size] [--format]
  ingest <path> [--dry-run] [--redact] [--format]
  graph [--format] [--root] [--depth] [--type] [--relation] [--output] [--cross-wiki]
  index rebuild [--dry-run] [--format]
  index status [--format]
  history <slug> [-n <limit>] [--no-follow] [--format]
  stats [--format]
  lint [--rules] [--severity] [--format]
  suggest <slug> [-n <limit>] [--format]
  schema list [--format]
  schema show <type> [--template]
  schema add <type> <schema-path>
  schema remove <type> [--delete] [--delete-pages] [--dry-run]
  schema validate [type]
  export [--path] [--format llms-txt|llms-full|json] [--status active|all]
  resolve <uri>
  serve [--http[:PORT]] [--web[:PORT]] [--acp] [--watch] [--dry-run]
  watch
  logs tail [--lines N]
  logs list
  logs clear
  info [--format]
`

type cli struct {
	wikiFlag  string
	configArg string
	engine    *wiki.WikiEngine
	logger    *slog.Logger
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(argv []string) int {
	if len(argv) == 0 {
		fmt.Print(usageText)
		return 0
	}
	var rest []string
	wikiFlag, configFlag := "", ""
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "--wiki" && i+1 < len(argv):
			wikiFlag = argv[i+1]
			i++
		case strings.HasPrefix(arg, "--wiki="):
			wikiFlag = strings.TrimPrefix(arg, "--wiki=")
		case arg == "--config" && i+1 < len(argv):
			configFlag = argv[i+1]
			i++
		case strings.HasPrefix(arg, "--config="):
			configFlag = strings.TrimPrefix(arg, "--config=")
		default:
			rest = append(rest, arg)
		}
	}
	if len(rest) == 0 {
		fmt.Print(usageText)
		return 0
	}

	c := &cli{
		wikiFlag:  wikiFlag,
		configArg: wiki.ConfigValuePath(configFlag),
		logger:    slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return c.dispatch(ctx, rest)
}

func (c *cli) engineOrDie() *wiki.WikiEngine {
	if c.engine == nil {
		e, err := wiki.BuildEngine(c.configArg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		c.engine = e
	}
	return c.engine
}

func (c *cli) wikiName() string {
	if c.wikiFlag != "" {
		return c.wikiFlag
	}
	return c.engineOrDie().DefaultWikiName()
}

func (c *cli) dispatch(ctx context.Context, args []string) int {
	cmd, sub := args[0], args[1:]
	switch cmd {
	case "spaces":
		return c.cmdSpaces(sub)
	case "config":
		return c.cmdConfig(sub)
	case "content":
		return c.cmdContent(sub)
	case "search":
		return c.cmdSearch(sub)
	case "list":
		return c.cmdList(sub)
	case "ingest":
		return c.cmdIngest(sub)
	case "graph":
		return c.cmdGraph(sub)
	case "index":
		return c.cmdIndex(sub)
	case "history":
		return c.cmdHistory(sub)
	case "stats":
		return c.cmdStats(sub)
	case "lint":
		return c.cmdLint(sub)
	case "suggest":
		return c.cmdSuggest(sub)
	case "schema":
		return c.cmdSchema(sub)
	case "export":
		return c.cmdExport(sub)
	case "resolve":
		return c.cmdResolve(sub)
	case "serve":
		return c.cmdServe(ctx, sub)
	case "watch":
		return c.cmdWatch(ctx, sub)
	case "logs":
		return c.cmdLogs(sub)
	case "info":
		return c.cmdInfo(sub)
	case "help", "--help", "-h":
		fmt.Print(usageText)
		return 0
	case "version", "--version":
		fmt.Println("llm-wiki-go 0.1.0")
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		fmt.Print(usageText)
		return 2
	}
}

// boolFlags never consume the following token — `search --semantic 注意力`
// must treat the query as positional, not as the flag's value.
var boolFlags = map[string]bool{
	"semantic": true, "hybrid": true, "no-excerpt": true, "all": true,
	"redact": true, "force": true, "cross-wiki": true, "no-frontmatter": true,
	"list-assets": true, "section": true, "bundle": true, "dry-run": true,
	"delete": true, "delete-pages": true, "template": true, "global": true,
	"no-follow": true, "include-sections": true, "set-default": true,
	"watch": true, "acp": true,
}

// parseFlags splits argv into (positional, flags). "--flag value" and
// "--flag=value" both work; bare flags are "true"; known boolean flags
// never swallow the next token.
func parseFlags(argv []string) ([]string, map[string]string) {
	var pos []string
	flags := map[string]string{}
	for i := 0; i < len(argv); i++ {
		if !strings.HasPrefix(argv[i], "-") || argv[i] == "-" {
			pos = append(pos, argv[i])
			continue
		}
		name := strings.TrimLeft(argv[i], "-")
		if eq := strings.IndexByte(name, '='); eq > 0 {
			flags[name[:eq]] = name[eq+1:]
			continue
		}
		if boolFlags[name] || (i+1 >= len(argv)) || strings.HasPrefix(argv[i+1], "-") {
			flags[name] = "true"
		} else {
			flags[name] = argv[i+1]
			i++
		}
	}
	return pos, flags
}

func flagBool(flags map[string]string, name string) bool {
	v, ok := flags[name]
	return ok && (v == "true" || v == "" || v == "1")
}

func flagInt(flags map[string]string, name string, def int) int {
	if v, ok := flags[name]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func printJSON(v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(raw))
	return nil
}

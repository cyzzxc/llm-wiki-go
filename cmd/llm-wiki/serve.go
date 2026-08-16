package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"llm-wiki-go/internal/acpserver"
	"llm-wiki-go/internal/mcpserver"
	"llm-wiki-go/internal/watch"
	"llm-wiki-go/internal/wiki"
)

// cmdServe runs the MCP server: stdio always; HTTP with --http[:PORT] or
// serve.http; ACP with --acp or serve.acp; watcher with --watch.
func (c *cli) cmdServe(ctx context.Context, args []string) int {
	_, flags := parseFlags(args)
	engine := c.engineOrDie()
	cfg := engine.State.Config.Serve

	httpEnabled := cfg.HTTP
	port := cfg.HTTPPort
	// --http, --http=PORT, and --http:PORT all select the HTTP transport.
	for name, v := range flags {
		if name == "http" || strings.HasPrefix(name, "http:") || strings.HasPrefix(name, "http=") {
			httpEnabled = true
			v = strings.TrimPrefix(strings.TrimPrefix(name[len("http"):], ":"), "=")
			if v == "" {
				v = strings.TrimPrefix(flags["http"], ":")
			}
			if v != "" && v != "true" {
				if p, err := strconv.Atoi(v); err == nil {
					port = p
				}
			}
		}
	}
	acpEnabled := cfg.ACP || flagBool(flags, "acp")
	watchEnabled := flagBool(flags, "watch")

	if flagBool(flags, "dry-run") {
		var parts []string
		parts = append(parts, "stdio")
		if httpEnabled {
			parts = append(parts, fmt.Sprintf("http :%d", port))
		}
		if acpEnabled {
			parts = append(parts, "acp")
		}
		if watchEnabled {
			parts = append(parts, "watch")
		}
		fmt.Printf("Would start: [%s]\n", strings.Join(parts, "] ["))
		return 0
	}

	logger := wiki.NewServeLogger(engine.State.Config.Logging)
	server := mcpserver.New(engine)

	var acpPush chan acpserver.Push
	if acpEnabled {
		acpPush = make(chan acpserver.Push, 64)
	}

	if watchEnabled {
		debounce := time.Duration(engine.State.Config.Watch.DebounceMs) * time.Millisecond
		w, err := watch.New(engine, debounce, logger)
		if err != nil {
			logger.Warn("watcher failed to start", "error", err)
		} else {
			go func() {
				for {
					select {
					case <-ctx.Done():
						return
					case ev := <-w.Events:
						for _, p := range ev.Paths {
							if _, _, err := wiki.OpsIngest(engine, ev.Wiki, p, false, false); err != nil {
								logger.Warn("auto-ingest failed", "wiki", ev.Wiki, "path", p, "error", err)
							}
						}
						if acpPush != nil {
							acpPush <- acpserver.Push{Wiki: ev.Wiki, Message: "wiki updated and re-ingested"}
						}
					}
				}
			}()
			defer w.Stop()
		}
	}

	if cfg.HeartbeatSecs > 0 {
		go func() {
			ticker := time.NewTicker(time.Duration(cfg.HeartbeatSecs) * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					logger.Debug("heartbeat")
				}
			}
		}()
	}

	var transports []string
	if acpEnabled {
		transports = append(transports, "acp")
	}
	if httpEnabled {
		transports = append(transports, fmt.Sprintf("http :%d", port))
	}
	if !acpEnabled && !httpEnabled {
		transports = append(transports, "stdio")
	}
	if watchEnabled {
		transports = append(transports, "watch")
	}
	logger.Info("server started", "wikis", len(engine.SpacesList()), "transports", fmt.Sprintf("[%s]", strings.Join(transports, "] [")))

	// Transport selection matches the Rust original: ACP owns stdio when
	// enabled; HTTP runs alongside ACP or alone; MCP stdio only when
	// neither ACP nor HTTP is active (stdio EOF would otherwise stop the
	// whole serve command).
	errCh := make(chan error, 3)
	switch {
	case acpEnabled:
		acp := acpserver.New(engine, cfg, logger, acpPush)
		go func() { errCh <- acp.Serve(ctx) }()
		if httpEnabled {
			go func() {
				errCh <- server.ServeHTTP(ctx, port, cfg.HTTPAllowedHosts, cfg.MaxRestarts, cfg.RestartBackoff)
			}()
		}
	case httpEnabled:
		go func() {
			errCh <- server.ServeHTTP(ctx, port, cfg.HTTPAllowedHosts, cfg.MaxRestarts, cfg.RestartBackoff)
		}()
	default:
		go func() { errCh <- server.ServeStdio(ctx) }()
	}

	select {
	case <-ctx.Done():
		logger.Info("server stopped")
		return 0
	case err := <-errCh:
		if err != nil && ctx.Err() == nil {
			logger.Error("server error", "error", err)
			return 1
		}
		logger.Info("server stopped")
		return 0
	}
}

// cmdWatch runs the standalone watcher (no server).
func (c *cli) cmdWatch(ctx context.Context, args []string) int {
	engine := c.engineOrDie()
	logger := wiki.NewServeLogger(engine.State.Config.Logging)
	debounce := time.Duration(engine.State.Config.Watch.DebounceMs) * time.Millisecond
	w, err := watch.New(engine, debounce, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer w.Stop()
	fmt.Println("Watching for changes (ctrl+c to stop)...")
	for {
		select {
		case <-ctx.Done():
			return 0
		case ev := <-w.Events:
			if report, _, err := wiki.OpsIngest(engine, ev.Wiki, ".", false, false); err != nil {
				logger.Warn("auto-ingest failed", "wiki", ev.Wiki, "paths", strings.Join(ev.Paths, ","), "error", err)
			} else {
				fmt.Printf("ingested %s: %d pages, %d warnings\n", strings.Join(ev.Paths, ", "), report.PagesValidated, len(report.Warnings))
			}
		}
	}
}

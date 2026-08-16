package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"llm-wiki-go/internal/acpserver"
	"llm-wiki-go/internal/mcpserver"
	"llm-wiki-go/internal/watch"
	"llm-wiki-go/internal/web"
	"llm-wiki-go/internal/wiki"
)

// cmdServe runs the MCP server: stdio always; HTTP with --http[:PORT] or
// serve.http; ACP with --acp or serve.acp; the web UI with --web[:PORT];
// watcher with --watch.
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
	// --web[:PORT] selects the read-only web UI on its own port (default
	// 8090, loopback-only). It coexists with every other transport.
	webEnabled := false
	webPort := 8090
	for name, v := range flags {
		if name == "web" || strings.HasPrefix(name, "web:") || strings.HasPrefix(name, "web=") {
			webEnabled = true
			v = strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(name, "web"), ":"), "=")
			if v == "" {
				v = strings.TrimPrefix(flags["web"], ":")
			}
			if v != "" && v != "true" {
				if p, err := strconv.Atoi(v); err == nil {
					webPort = p
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
		if webEnabled {
			parts = append(parts, fmt.Sprintf("web :%d", webPort))
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
	if webEnabled {
		transports = append(transports, fmt.Sprintf("web :%d", webPort))
	}
	if !acpEnabled && !httpEnabled && !webEnabled {
		transports = append(transports, "stdio")
	}
	if watchEnabled {
		transports = append(transports, "watch")
	}
	logger.Info("server started", "wikis", len(engine.SpacesList()), "transports", fmt.Sprintf("[%s]", strings.Join(transports, "] [")))

	// Transport selection matches the Rust original: ACP owns stdio when
	// enabled; HTTP runs alongside ACP or alone; MCP stdio only when no
	// server transport is active — stdio EOF (e.g. backgrounded serve)
	// would otherwise stop the whole command, so the web UI suppresses it
	// too.
	errCh := make(chan error, 4)
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
	case !webEnabled:
		go func() { errCh <- server.ServeStdio(ctx) }()
	}

	if webEnabled {
		ui := web.New(engine, c.wikiName())
		webSrv := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", webPort), Handler: ui}
		go func() {
			if err := webSrv.ListenAndServe(); err != nil && ctx.Err() == nil {
				errCh <- err
			}
		}()
		go func() {
			<-ctx.Done()
			webSrv.Close()
		}()
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

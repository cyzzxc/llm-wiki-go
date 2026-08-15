// Package acpserver implements the agent side of the Agent Client
// Protocol (ACP v1) over stdio JSON-RPC: session lifecycle, prompts
// dispatched to wiki workflows (research / lint / graph / ingest / use),
// streaming session/update notifications, and cooperative cancellation.
package acpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"llm-wiki-go/internal/mcpserver"
	"llm-wiki-go/internal/wiki"
)

// Session is one active ACP session.
type Session struct {
	ID        string
	Label     string
	Wiki      string
	CreatedAt int64

	mu        sync.Mutex
	activeRun string
	cancelled atomic.Bool
}

// Server is the ACP agent.
type Server struct {
	Engine   *wiki.WikiEngine
	ServeCfg wiki.ServeConfig
	Log      *slog.Logger

	mu       sync.Mutex
	sessions map[string]*Session

	// Push channel receives (wiki, message) events from the watcher.
	PushCh <-chan Push

	outMu sync.Mutex
	w     io.Writer
}

// Push is a watcher event forwarded to idle ACP sessions.
type Push struct {
	Wiki    string
	Message string
}

// New creates the ACP server.
func New(engine *wiki.WikiEngine, cfg wiki.ServeConfig, log *slog.Logger, push <-chan Push) *Server {
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	s := &Server{Engine: engine, ServeCfg: cfg, Log: log, sessions: map[string]*Session{}}
	s.PushCh = push
	return s
}

// ── JSON-RPC plumbing ────────────────────────────────────────────────────────

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve runs the agent loop on stdin/stdout until ctx is done or stdin EOF.
func (s *Server) Serve(ctx context.Context) error {
	return s.ServeOn(ctx, os.Stdin, os.Stdout)
}

// ServeOn runs the agent loop over the given streams (testable).
func (s *Server) ServeOn(ctx context.Context, in io.Reader, out io.Writer) error {
	s.w = out
	go s.pushLoop(ctx)

	reader := bufio.NewReader(in)
	dec := json.NewDecoder(reader)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var msg rpcMessage
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("ACP error: %v", err)
		}
		s.handle(ctx, msg)
	}
}

func (s *Server) handle(ctx context.Context, msg rpcMessage) {
	switch msg.Method {
	case "initialize":
		s.respond(msg.ID, map[string]any{
			"protocolVersion": 1,
			"agentCapabilities": map[string]any{
				"loadSession":         true,
				"promptCapabilities":  map[string]any{},
				"sessionCapabilities": map[string]any{"list": map[string]any{}},
			},
			"agentInfo": map[string]any{"name": "llm-wiki", "version": mcpserver.Version},
		})
	case "session/new":
		s.handleNewSession(msg)
	case "session/load":
		var p struct {
			SessionID string `json:"sessionId"`
		}
		json.Unmarshal(msg.Params, &p)
		if s.getSession(p.SessionID) != nil {
			s.respond(msg.ID, map[string]any{})
		} else {
			s.respondErr(msg.ID, -32602, fmt.Sprintf("session %s not found", p.SessionID))
		}
	case "session/list":
		type info struct {
			SessionID string `json:"sessionId"`
			Cwd       string `json:"cwd"`
			Title     string `json:"title,omitempty"`
		}
		cwd := s.sessionCwd()
		var infos []info
		s.mu.Lock()
		for _, sess := range s.sessions {
			i := info{SessionID: sess.ID, Cwd: cwd, Title: sess.Label}
			if sess.activeRun != "" {
				i.Title = "[active] " + sess.Label
			}
			infos = append(infos, i)
		}
		s.mu.Unlock()
		s.respond(msg.ID, map[string]any{"sessions": infos})
	case "session/prompt":
		s.handlePrompt(ctx, msg)
	case "session/cancel":
		var p struct {
			SessionID string `json:"sessionId"`
		}
		json.Unmarshal(msg.Params, &p)
		if sess := s.getSession(p.SessionID); sess != nil {
			sess.cancelled.Store(true)
			s.clearActiveRun(p.SessionID)
		}
		if len(msg.ID) > 0 {
			s.respond(msg.ID, map[string]any{})
		}
	default:
		if len(msg.ID) > 0 {
			s.respondErr(msg.ID, -32601, "not supported")
		}
	}
}

// ── Session handlers ─────────────────────────────────────────────────────────

func (s *Server) handleNewSession(msg rpcMessage) {
	var p struct {
		Meta  map[string]any `json:"mcpSessionServers"`
		Meta2 map[string]any `json:"meta"`
	}
	json.Unmarshal(msg.Params, &p)

	s.mu.Lock()
	count := len(s.sessions)
	s.mu.Unlock()
	if count >= s.ServeCfg.ACPMaxSessions {
		s.respondErr(msg.ID, -32602, fmt.Sprintf("Session limit reached (max: %d)", s.ServeCfg.ACPMaxSessions))
		return
	}
	id := fmt.Sprintf("session-%d", time.Now().UnixMilli())
	wikiName := ""
	if v, ok := p.Meta2["wiki"].(string); ok {
		wikiName = v
	}
	sess := &Session{ID: id, Wiki: wikiName, CreatedAt: time.Now().Unix()}
	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()
	s.Log.Info("session created", "session", id)
	s.respond(msg.ID, map[string]any{"sessionId": id})
}

func (s *Server) handlePrompt(ctx context.Context, msg rpcMessage) {
	var p struct {
		SessionID string `json:"sessionId"`
		Prompt    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"prompt"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		s.respondErr(msg.ID, -32602, err.Error())
		return
	}
	var textParts []string
	for _, b := range p.Prompt {
		if b.Type == "text" {
			textParts = append(textParts, b.Text)
		}
	}
	text := joinNonEmpty(textParts, " ")

	sess := s.getSession(p.SessionID)
	if sess == nil {
		s.respondErr(msg.ID, -32602, fmt.Sprintf("session %s not found", p.SessionID))
		return
	}
	sess.cancelled.Store(false)
	sess.mu.Lock()
	sess.activeRun = fmt.Sprintf("run-%d", time.Now().UnixMilli())
	sess.mu.Unlock()

	workflow, query := dispatchWorkflow(text)
	wikiName := s.resolveWikiName(sess)
	if query == "" {
		query = text
	}

	switch workflow {
	case "research":
		s.runResearch(p.SessionID, query, wikiName)
	case "lint":
		s.runLint(p.SessionID, query, wikiName)
	case "graph":
		s.runGraph(p.SessionID, query, wikiName)
	case "ingest":
		s.runIngest(p.SessionID, query, wikiName)
	case "use":
		if query == "" {
			s.sendText(p.SessionID, "Usage: `llm-wiki:use <slug>`")
		} else {
			s.stepRead(p.SessionID, "use", query, wikiName, true)
		}
		s.clearActiveRun(p.SessionID)
	default:
		help := "Available workflows:\n" +
			"• `llm-wiki:research <query>` — search + read top result\n" +
			"• `llm-wiki:lint [rules]`      — run lint rules (comma-separated or all)\n" +
			"• `llm-wiki:graph [root-slug]` — render concept graph\n" +
			"• `llm-wiki:ingest [path]`     — ingest path (default: cwd)\n" +
			"• `llm-wiki:use <slug>`        — read full page content\n" +
			"• `llm-wiki:help`              — this message\n" +
			"• (bare prompt)                — research workflow"
		if workflow != "help" {
			help = fmt.Sprintf("Unknown workflow %q. %s", workflow, help)
		}
		s.sendText(p.SessionID, help)
		s.clearActiveRun(p.SessionID)
	}

	s.respond(msg.ID, map[string]any{"stopReason": "end_turn"})
}

func (s *Server) getSession(id string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

func (s *Server) clearActiveRun(id string) {
	if sess := s.getSession(id); sess != nil {
		sess.mu.Lock()
		sess.activeRun = ""
		sess.mu.Unlock()
	}
}

func (s *Server) isCancelled(id string) bool {
	if sess := s.getSession(id); sess != nil {
		return sess.cancelled.Load()
	}
	return false
}

func (s *Server) resolveWikiName(sess *Session) string {
	name := ""
	if sess != nil && sess.Wiki != "" {
		name = sess.Wiki
	}
	if name == "" {
		name = s.Engine.DefaultWikiName()
	}
	return name
}

func (s *Server) sessionCwd() string {
	if sp, err := s.Engine.ResolveWiki(""); err == nil {
		return sp.RepoRoot
	}
	return "."
}

// dispatchWorkflow parses "llm-wiki:<workflow> <text>" or defaults to research.
func dispatchWorkflow(prompt string) (string, string) {
	if rest, ok := cutPrefix(prompt, "llm-wiki:"); ok {
		rest = trimLeadingSpace(rest)
		if pos := indexAnySpace(rest); pos >= 0 {
			return rest[:pos], trimLeadingSpace(rest[pos:])
		}
		return rest, ""
	}
	return "research", prompt
}

// ── Streaming helpers (session/update notifications) ─────────────────────────

func (s *Server) notify(sessionID string, update map[string]any) {
	notif := map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"sessionId": sessionID,
			"update":    update,
		},
	}
	raw, err := json.Marshal(notif)
	if err != nil {
		return
	}
	s.outMu.Lock()
	defer s.outMu.Unlock()
	if s.w != nil {
		s.w.Write(append(raw, '\n'))
	}
}

func (s *Server) sendText(sessionID, text string) {
	s.notify(sessionID, map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": text},
	})
}

func (s *Server) sendToolCall(sessionID, id, title, kind string) {
	s.notify(sessionID, map[string]any{
		"sessionUpdate": "tool_call",
		"toolCallId":    id,
		"title":         title,
		"kind":          kind,
		"status":        "in_progress",
	})
}

func (s *Server) sendToolResult(sessionID, id, status, content string) {
	s.notify(sessionID, map[string]any{
		"sessionUpdate": "tool_call_update",
		"toolCallId":    id,
		"status":        status,
		"content":       []map[string]any{{"type": "text", "text": content}},
	})
}

func (s *Server) respond(id json.RawMessage, result any) {
	resp := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result}
	raw, _ := json.Marshal(resp)
	s.outMu.Lock()
	defer s.outMu.Unlock()
	if s.w != nil {
		s.w.Write(append(raw, '\n'))
	}
}

func (s *Server) respondErr(id json.RawMessage, code int, message string) {
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"error":   rpcError{Code: code, Message: message},
	}
	raw, _ := json.Marshal(resp)
	s.outMu.Lock()
	defer s.outMu.Unlock()
	if s.w != nil {
		s.w.Write(append(raw, '\n'))
	}
}

// pushLoop forwards watcher pushes to idle sessions of the same wiki.
func (s *Server) pushLoop(ctx context.Context) {
	if s.PushCh == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case p, ok := <-s.PushCh:
			if !ok {
				return
			}
			s.mu.Lock()
			for _, sess := range s.sessions {
				if sess.Wiki == p.Wiki && sess.activeRun == "" {
					s.sendText(sess.ID, p.Message)
				}
			}
			s.mu.Unlock()
		}
	}
}

func makeToolID(workflow, step string) string {
	return fmt.Sprintf("%s-%s-%d", workflow, step, time.Now().UnixMilli())
}

func joinNonEmpty(parts []string, sep string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out != "" {
			out += sep
		}
		out += p
	}
	return out
}

func cutPrefix(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):], true
	}
	return s, false
}

func trimLeadingSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	return s
}

func indexAnySpace(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' || s[i] == '\n' {
			return i
		}
	}
	return -1
}

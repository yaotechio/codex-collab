// codexmcp: a thin MCP server bridging Claude Code and the local Codex CLI.
//
// Three modes:
//
//	codexmcp          -> run as a stdio MCP server exposing the `codex` tool
//	codexmcp hook     -> PreToolUse hook: ask for confirmation on write-mode calls
//	codexmcp fmt      -> format `codex exec --json` JSONL for Bash pipes
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

var (
	timeout time.Duration
	counter *rounds
	// errIdle: codex produced no output for `timeout` and was killed as stuck.
	errIdle = errors.New("idle timeout")
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "hook" {
		runHook()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "fmt" {
		runFmt()
		return
	}

	timeout = time.Duration(envIntMin("CODEX_MCP_TIMEOUT", 300, 1)) * time.Second
	counter = &rounds{
		data: map[string]*entry{},
		max:  envIntMin("CODEX_MCP_MAX_ROUNDS", 6, 1),
		ttl:  time.Duration(envIntMin("CODEX_MCP_SESSION_TTL", 24, 1)) * time.Hour,
	}

	s := server.NewMCPServer("codex-collab", "0.1.0")
	tool := mcp.NewTool("codex",
		mcp.WithDescription("调用本机 Codex CLI 进行底层代码分析/实现。协作约定：先用 sandbox=read-only 多轮讨论方案（复用同一 session_id），定稿并经用户确认后，再用 sandbox=workspace-write 且 confirmed=true（不传 session_id，新起会话）派 Codex 实现，最后对照方案验收。"),
		mcp.WithString("PROMPT", mcp.Required(), mcp.Description("发给 Codex 的任务指令")),
		mcp.WithString("cd", mcp.Description("Codex 工作目录，默认当前目录 .")),
		mcp.WithString("sandbox", mcp.Description("read-only(默认) / workspace-write / danger-full-access")),
		mcp.WithString("session_id", mcp.Description("传入则续接该会话（保留上下文）；不传则新建会话")),
		mcp.WithBoolean("skip_git_repo_check", mcp.Description("允许在非 git 仓库执行")),
		mcp.WithString("model", mcp.Description("指定 Codex 模型；空则用 codex 默认配置")),
		mcp.WithBoolean("return_all_messages", mcp.Description("true 时返回 Codex 完整推理过程与工具调用事件")),
		mcp.WithBoolean("confirmed", mcp.Description("写操作闸门：sandbox 为 workspace-write/danger-full-access 时必须为 true")),
	)
	s.AddTool(tool, handle)

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintln(os.Stderr, "codexmcp server error:", err)
		os.Exit(1)
	}
}

// ---- tool handler ----

type out struct {
	Success         bool     `json:"success"`
	SessionID       string   `json:"session_id,omitempty"`
	Output          []string `json:"output,omitempty"`  // codex 最终回复，按行拆分以便展开时逐行显示
	Process         []string `json:"process,omitempty"` // codex 的思考/执行过程轨迹（每元素一步，展开时各占一行）
	Round           int      `json:"round,omitempty"`
	RoundsRemaining int      `json:"rounds_remaining"`
	LogFile         string   `json:"log_file,omitempty"`
	Error           string   `json:"error,omitempty"`
}

func handle(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	a := req.Params.Arguments
	prompt := getStr(a, "PROMPT", "")
	if strings.TrimSpace(prompt) == "" {
		return result(out{Success: false, Error: "PROMPT 不能为空"})
	}
	cd := getStr(a, "cd", ".")
	sandbox := getStr(a, "sandbox", "read-only")
	sessionID := getStr(a, "session_id", "")
	model := getStr(a, "model", "")
	skipGit := getBool(a, "skip_git_repo_check", false)
	returnAll := getBool(a, "return_all_messages", false)
	confirmed := getBool(a, "confirmed", false)

	// ① write gate: irreversible writes require explicit confirmation.
	effectiveSandbox := sandbox
	if sessionID == "" {
		if isWriteSandbox(sandbox) && !confirmed {
			return result(out{Success: false, Error: fmt.Sprintf("写操作被拒绝：sandbox=%s 需要 confirmed=true（须先与用户确认定稿方案）", sandbox)})
		}
	} else if stored, ok := counter.sandboxOf(sessionID); ok {
		effectiveSandbox = stored
		if isWriteSandbox(stored) && !confirmed {
			return result(out{Success: false, SessionID: sessionID, Error: fmt.Sprintf("写操作被拒绝：sandbox=%s 需要 confirmed=true（须先与用户确认定稿方案）", stored)})
		}
	} else {
		effectiveSandbox = "danger-full-access"
		if !confirmed {
			return result(out{Success: false, SessionID: sessionID, Error: "原会话 sandbox 未知（服务可能已重启），请传 confirmed=true 或新建会话"})
		}
	}

	// round cap (only resume sessions can exceed; a new session is always round 1).
	if ok, reason := counter.check(sessionID); !ok {
		return result(out{Success: false, SessionID: sessionID, RoundsRemaining: 0, Error: reason})
	}

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Live progress: if the client passed a progressToken, relay each codex
	// event to it as a notifications/progress so Claude Code shows codex's
	// steps under the spinner instead of a blank wait. Same lines as the log.
	// ponytail: dropped notifications (blocked channel) are fine — log is truth.
	var notify func(string)
	if req.Params.Meta != nil && req.Params.Meta.ProgressToken != nil {
		if srv := server.ServerFromContext(ctx); srv != nil {
			token := req.Params.Meta.ProgressToken
			var n float64
			notify = func(msg string) {
				n++
				_ = srv.SendNotificationToClient(ctx, "notifications/progress", map[string]any{
					"progressToken": token, "progress": n, "message": msg,
				})
			}
		}
	}

	// One log file per session so concurrent windows don't interleave. Resume
	// rounds reuse the session's file; a fresh session gets a unique temp tag.
	tag := sessionID
	if tag == "" {
		tag = "new-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	lp := logPath(tag)

	sid, msg, raw, stderr, process, err := runCodex(cctx, params{
		prompt: prompt, cd: cd, sandbox: sandbox, sessionID: sessionID, model: model, skipGit: skipGit, logFile: lp, notify: notify,
	})
	if err != nil {
		emsg := strings.TrimSpace(stderr)
		if errors.Is(err, errIdle) {
			emsg = fmt.Sprintf("codex 静默超过 %s 无任何输出，已判定为卡死并终止", timeout)
		} else if emsg == "" {
			emsg = err.Error()
		} else {
			emsg = tailRunes(emsg, 800)
		}
		// even on failure, hand back whatever process trace we captured so the
		// user sees how far codex got before dying.
		return result(out{Success: false, SessionID: sid, Process: process, LogFile: lp, Error: "codex 执行失败：" + emsg})
	}

	finalID := sid
	if finalID == "" {
		finalID = sessionID
	}
	if sessionID == "" && sid == "" {
		return result(out{Success: false, Process: process, LogFile: lp, Error: "codex 未返回会话 id（thread.started 缺失），无法续接"})
	}
	// New session: rename the temp-tagged log to the real session id so every
	// log file is named codexmcp-<sessionId>.log. (codex has exited; file closed.)
	if sessionID == "" && sid != "" {
		if np := logPath(sid); os.Rename(lp, np) == nil {
			lp = np
		}
	}
	round, remaining := counter.commit(finalID, effectiveSandbox)
	body := msg
	if returnAll {
		body = raw
	}
	return result(out{Success: true, SessionID: finalID, Output: splitLines(body), Process: process, Round: round, RoundsRemaining: remaining, LogFile: lp})
}

// ---- codex invocation ----

type params struct {
	prompt, cd, sandbox, sessionID, model, logFile string
	skipGit                                        bool
	notify                                         func(string) // per-event progress relay; nil if client wants none
}

func runCodex(ctx context.Context, p params) (sessionID, lastMsg, raw, stderr string, process []string, err error) {
	var args []string
	if p.sessionID == "" {
		// new session: sandbox & cwd are set here and locked for the session's life
		args = []string{"exec", "--json", "-C", p.cd, "-s", p.sandbox}
	} else {
		// resume: reads PROMPT from stdin via `-`; sandbox/cwd inherited from the session
		args = []string{"exec", "resume", p.sessionID, "-", "--json"}
	}
	args = append(args, "-c", "model_reasoning_summary=detailed")
	if p.model != "" {
		args = append(args, "-m", p.model)
	}
	if p.skipGit {
		args = append(args, "--skip-git-repo-check")
	}

	// exec.CommandContext resolves `codex` via PATH on all OSes (Windows honors
	// PATHEXT, so codex.cmd/.exe resolve too) and kills it on ctx cancel/timeout.
	// ponytail: kills the direct child only; add per-OS process-group kill if
	// codex's sandbox grandchildren are seen to orphan.
	cmd := exec.CommandContext(ctx, "codex", args...)
	cmd.Stdin = strings.NewReader(p.prompt)
	var se bytes.Buffer
	cmd.Stderr = &se
	pipe, perr := cmd.StdoutPipe()
	if perr != nil {
		return "", "", "", "", nil, perr
	}

	// tee codex's JSONL stdout to a log file as it streams, so the user can
	// `tail -f` it and see Codex is alive instead of staring at a spinner.
	// ponytail: single fixed file, append mode — `tail -f` follows the latest run.
	lf, _ := os.OpenFile(p.logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if lf != nil {
		defer lf.Close()
		fmt.Fprintf(lf, "\n==== %s  sandbox=%s  cwd=%s ====\n>> %s\n",
			time.Now().Format("15:04:05"), p.sandbox, p.cd, firstLine(p.prompt))
	}

	if err = cmd.Start(); err != nil {
		return
	}

	// Idle watchdog: codex reads files one-by-one and can run well past a fixed
	// total timeout while genuinely working. It emits a JSONL event per step, so
	// treat *silence*, not elapsed time, as "stuck": reset the deadline on every
	// line and only kill after `timeout` with zero output.
	// ponytail: no absolute cap — round counter bounds the conversation; add one
	// here if a single exec is ever seen to stream forever.
	var idledOut atomic.Bool
	wd := time.AfterFunc(timeout, func() { idledOut.Store(true); _ = cmd.Process.Kill() })
	defer wd.Stop()

	var so bytes.Buffer
	sc := bufio.NewScanner(pipe)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		wd.Reset(timeout)
		so.Write(sc.Bytes())
		so.WriteByte('\n')
		if s := fmtEvent(sc.Bytes()); strings.TrimSpace(s) != "" {
			// process accumulates the same human-readable lines we stream live, so
			// the final result carries codex's full thinking/execution trace — one
			// element per step so it renders one-per-line when the result expands.
			process = append(process, s)
			if lf != nil {
				fmt.Fprintln(lf, s)
			}
			if p.notify != nil {
				p.notify(s)
			}
		}
	}
	scanErr := sc.Err()
	if scanErr != nil {
		_ = cmd.Process.Kill()
	}
	err = cmd.Wait()
	if idledOut.Load() {
		err = errIdle
	} else if scanErr != nil {
		err = scanErr
	}

	sessionID, lastMsg, raw = parseEvents(so.Bytes())
	stderr = se.String()
	return
}

// logPath maps a session tag to its own log file so concurrent sessions never
// interleave. tag is sanitized to keep it a single safe filename component.
func logPath(tag string) string {
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return '_'
	}, tag)
	return filepath.Join(os.TempDir(), "codexmcp-"+safe+".log")
}

func runFmt() {
	var sessionID, lastMsg string
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		if s := fmtEvent(sc.Bytes()); strings.TrimSpace(s) != "" {
			fmt.Fprintln(os.Stdout, s)
		}
		var e codexEvent
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		switch e.Type {
		case "thread.started":
			if e.ThreadID != "" {
				sessionID = e.ThreadID
			}
		case "item.completed":
			if e.Item.Type == "agent_message" {
				lastMsg = e.Item.Text
			}
		}
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "codexmcp fmt warning:", err)
	}
	fmt.Fprintln(os.Stdout, "__CODEXMCP_SESSION__="+sessionID)
	fmt.Fprintln(os.Stdout, "__CODEXMCP_FINAL_B64__="+base64.StdEncoding.EncodeToString([]byte(lastMsg)))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return truncRunes(s, 120)
}

// oneLine flattens any internal newlines/whitespace runs into single spaces so
// a trace element stays strictly one line when the result is expanded.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// fmtEvent turns one JSONL line into a short human-readable progress line.
func fmtEvent(b []byte) string {
	var e codexEvent
	if json.Unmarshal(b, &e) != nil {
		return ""
	}
	switch e.Type {
	case "thread.started":
		return "▶ session " + e.ThreadID
	case "item.started":
		if e.Item.Type == "command_execution" && e.Item.Command != "" {
			return "$ " + firstLine(e.Item.Command)
		}
		return ""
	case "item.completed":
		// collapse internal newlines/whitespace so each trace element is one line
		t := oneLine(e.Item.Text)
		t = truncRunes(t, 200)
		if e.Item.Type == "agent_message" {
			return "💬 " + t
		}
		if e.Item.Type == "reasoning" {
			return "🤔 " + t
		}
		if e.Item.Type == "command_execution" {
			return ""
		}
		if t != "" {
			return "· " + e.Item.Type + ": " + t
		}
		return "· " + e.Item.Type
	case "turn.completed":
		return "✓ done"
	}
	return ""
}

type codexEvent struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Item     struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Command string `json:"command"`
	} `json:"item"`
}

// parseEvents reads `codex exec --json` JSONL stdout.
// session id  = thread.started.thread_id
// final reply = last item.completed whose item.type == "agent_message"
func parseEvents(b []byte) (sessionID, lastMsg, raw string) {
	raw = string(b)
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 16<<20) // tolerate long lines
	for sc.Scan() {
		var e codexEvent
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		switch e.Type {
		case "thread.started":
			if e.ThreadID != "" {
				sessionID = e.ThreadID
			}
		case "item.completed":
			if e.Item.Type == "agent_message" {
				lastMsg = e.Item.Text
			}
		}
	}
	return
}

// ---- round counter (in-memory; resets on restart) ----

type entry struct {
	count   int
	last    time.Time
	sandbox string
}

type rounds struct {
	mu   sync.Mutex
	data map[string]*entry
	max  int
	ttl  time.Duration
}

// check rejects a resume that would exceed max rounds. A new session (empty id)
// is always allowed — it becomes round 1 at commit time.
func (c *rounds) check(id string) (bool, string) {
	if id == "" {
		return true, ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweep()
	cur := 0
	if e := c.data[id]; e != nil {
		cur = e.count
	}
	if cur+1 > c.max {
		return false, fmt.Sprintf("已达单会话最大讨论轮次 %d，请定稿落地或另起会话", c.max)
	}
	return true, ""
}

func (c *rounds) sandboxOf(id string) (string, bool) {
	if id == "" {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweep()
	e := c.data[id]
	if e == nil {
		return "", false
	}
	return e.sandbox, true
}

func (c *rounds) commit(id, sandbox string) (round, remaining int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweep()
	e := c.data[id]
	if e == nil {
		e = &entry{sandbox: sandbox}
		c.data[id] = e
	}
	e.count++
	e.last = time.Now()
	return e.count, max(c.max-e.count, 0)
}

// sweep drops expired sessions. Caller must hold the lock.
func (c *rounds) sweep() {
	cutoff := time.Now().Add(-c.ttl)
	for k, e := range c.data {
		if e.last.Before(cutoff) {
			delete(c.data, k)
		}
	}
}

// ---- PreToolUse hook ----

func runHook() {
	var in struct {
		ToolInput map[string]any `json:"tool_input"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil || in.ToolInput == nil {
		hookAsk()
		os.Exit(0)
	}
	sandbox, _ := in.ToolInput["sandbox"].(string)
	if isWriteSandbox(sandbox) {
		hookAsk()
	}
	os.Exit(0)
}

// ---- helpers ----

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envIntMin(key string, def, min int) int {
	n := envInt(key, def)
	if n < min {
		return def
	}
	return n
}

func isWriteSandbox(s string) bool {
	return s == "workspace-write" || s == "danger-full-access"
}

func hookAsk() {
	fmt.Println(`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask","permissionDecisionReason":"Codex 即将以写模式修改文件，请确认方案已定稿后再批准。"}}`)
}

func truncRunes(s string, n int) string {
	if n <= 0 {
		if s == "" {
			return ""
		}
		return "…"
	}
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func tailRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) > n {
		return string(r[len(r)-n:])
	}
	return s
}

func getStr(m map[string]any, k, def string) string {
	if v, ok := m[k]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return def
}

func getBool(m map[string]any, k string, def bool) bool {
	if v, ok := m[k]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

// splitLines breaks a multi-line string into per-line elements so it renders
// one-per-line when the JSON result is expanded; empty/blank lines are dropped.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	var lines []string
	for _, ln := range strings.Split(s, "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		lines = append(lines, ln)
	}
	return lines
}

func result(o out) (*mcp.CallToolResult, error) {
	b, _ := json.MarshalIndent(o, "", "  ")
	return mcp.NewToolResultText(string(b)), nil
}

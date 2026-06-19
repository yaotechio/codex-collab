// codexmcp: a thin MCP server bridging Claude Code and the local Codex CLI.
//
// Two modes:
//   codexmcp          -> run as a stdio MCP server exposing the `codex` tool
//   codexmcp hook     -> PreToolUse hook: ask for confirmation on write-mode calls
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

var (
	timeout time.Duration
	counter *rounds
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "hook" {
		runHook()
		return
	}

	timeout = time.Duration(envInt("CODEX_MCP_TIMEOUT", 300)) * time.Second
	counter = &rounds{
		data: map[string]*entry{},
		max:  envInt("CODEX_MCP_MAX_ROUNDS", 6),
		ttl:  time.Duration(envInt("CODEX_MCP_SESSION_TTL", 24)) * time.Hour,
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
	Success         bool   `json:"success"`
	SessionID       string `json:"session_id,omitempty"`
	Output          string `json:"output,omitempty"`
	Round           int    `json:"round,omitempty"`
	RoundsRemaining int    `json:"rounds_remaining"`
	Error           string `json:"error,omitempty"`
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
	if (sandbox == "workspace-write" || sandbox == "danger-full-access") && !confirmed {
		return result(out{Success: false, Error: fmt.Sprintf("写操作被拒绝：sandbox=%s 需要 confirmed=true（须先与用户确认定稿方案）", sandbox)})
	}

	// round cap (only resume sessions can exceed; a new session is always round 1).
	if ok, reason := counter.check(sessionID); !ok {
		return result(out{Success: false, SessionID: sessionID, RoundsRemaining: 0, Error: reason})
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sid, msg, raw, stderr, err := runCodex(cctx, params{
		prompt: prompt, cd: cd, sandbox: sandbox, sessionID: sessionID, model: model, skipGit: skipGit,
	})
	if err != nil {
		emsg := strings.TrimSpace(stderr)
		if cctx.Err() == context.DeadlineExceeded {
			emsg = fmt.Sprintf("codex 执行超时（%s）", timeout)
		} else if emsg == "" {
			emsg = err.Error()
		} else if len(emsg) > 800 {
			emsg = emsg[len(emsg)-800:]
		}
		return result(out{Success: false, SessionID: sid, Error: "codex 执行失败：" + emsg})
	}

	finalID := sid
	if finalID == "" {
		finalID = sessionID
	}
	round, remaining := counter.commit(finalID)
	body := msg
	if returnAll {
		body = raw
	}
	return result(out{Success: true, SessionID: finalID, Output: body, Round: round, RoundsRemaining: remaining})
}

// ---- codex invocation ----

type params struct {
	prompt, cd, sandbox, sessionID, model string
	skipGit                               bool
}

func runCodex(ctx context.Context, p params) (sessionID, lastMsg, raw, stderr string, err error) {
	var args []string
	if p.sessionID == "" {
		// new session: sandbox & cwd are set here and locked for the session's life
		args = []string{"exec", "--json", "-C", p.cd, "-s", p.sandbox}
	} else {
		// resume: reads PROMPT from stdin via `-`; sandbox/cwd inherited from the session
		args = []string{"exec", "resume", p.sessionID, "-", "--json"}
	}
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
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err = cmd.Run()

	sessionID, lastMsg, raw = parseEvents(so.Bytes())
	stderr = se.String()
	return
}

type codexEvent struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Item     struct {
		Type string `json:"type"`
		Text string `json:"text"`
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
	count int
	last  time.Time
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

func (c *rounds) commit(id string) (round, remaining int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.data[id]
	if e == nil {
		e = &entry{}
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
	_ = json.NewDecoder(os.Stdin).Decode(&in)
	sandbox, _ := in.ToolInput["sandbox"].(string)
	if sandbox == "workspace-write" || sandbox == "danger-full-access" {
		fmt.Println(`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask","permissionDecisionReason":"Codex 即将以写模式修改文件，请确认方案已定稿后再批准。"}}`)
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

func result(o out) (*mcp.CallToolResult, error) {
	b, _ := json.Marshal(o)
	return mcp.NewToolResultText(string(b)), nil
}

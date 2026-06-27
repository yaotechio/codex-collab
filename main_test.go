package main

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// parseEvents must pull session id from thread.started and the final reply from
// the last agent_message item — matching real `codex exec --json` stdout.
func TestParseEvents(t *testing.T) {
	stdout := `{"type":"thread.started","thread_id":"abc-123"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"i0","type":"reasoning","text":"thinking"}}
{"type":"item.completed","item":{"id":"i1","type":"agent_message","text":"HELLO"}}
{"type":"turn.completed","usage":{"output_tokens":3}}`

	sid, msg, raw := parseEvents([]byte(stdout))
	if sid != "abc-123" {
		t.Errorf("session id = %q, want abc-123", sid)
	}
	if msg != "HELLO" {
		t.Errorf("final msg = %q, want HELLO", msg)
	}
	if raw != stdout {
		t.Errorf("raw should echo full stdout")
	}
}

// fmtEvent turns JSONL lines into short progress lines; noise lines drop to "".
func TestFmtEvent(t *testing.T) {
	cases := map[string]string{
		`{"type":"thread.started","thread_id":"x1"}`:                                            "▶ session x1",
		`{"type":"item.started","item":{"type":"command_execution","command":"ls -la\nfoo"}}`:   "$ ls -la",
		`{"type":"item.completed","item":{"type":"agent_message","text":"hi"}}`:                 "💬 hi",
		`{"type":"item.completed","item":{"type":"reasoning","text":"thinking"}}`:               "🤔 thinking",
		`{"type":"item.completed","item":{"type":"reasoning","text":"line1\n\nline2"}}`:         "🤔 line1 line2",
		`{"type":"item.completed","item":{"type":"command_execution","text":""}}`:               "",
		`{"type":"item.completed","item":{"type":"command_execution","command":"ls -la\nfoo"}}`: "",
		`{"type":"turn.completed"}`: "✓ done",
		`{"type":"turn.started"}`:   "",
		`not json`:                  "",
	}
	for in, want := range cases {
		if got := fmtEvent([]byte(in)); got != want {
			t.Errorf("fmtEvent(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestRunFmtFooter(t *testing.T) {
	stdin := `{"type":"thread.started","thread_id":"sid-1"}
{"type":"item.completed","item":{"type":"agent_message","text":"final"}}`

	oldIn, oldOut := os.Stdin, os.Stdout
	defer func() {
		os.Stdin = oldIn
		os.Stdout = oldOut
	}()

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inW.WriteString(stdin); err != nil {
		t.Fatal(err)
	}
	if err := inW.Close(); err != nil {
		t.Fatal(err)
	}

	os.Stdin = inR
	os.Stdout = outW
	runFmt()
	if err := outW.Close(); err != nil {
		t.Fatal(err)
	}

	b, err := io.ReadAll(outR)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, "__CODEXMCP_SESSION__=sid-1\n") {
		t.Errorf("missing session footer in %q", got)
	}
	if !strings.Contains(got, "__CODEXMCP_FINAL_B64__=ZmluYWw=\n") {
		t.Errorf("missing final footer in %q", got)
	}
}

func TestRuneSafeTruncation(t *testing.T) {
	mixed := strings.Repeat("a你", 80)
	got := firstLine(mixed)
	if !utf8.ValidString(got) {
		t.Fatalf("firstLine returned invalid UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != 121 {
		t.Fatalf("firstLine rune count = %d, want 121", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("firstLine should append ellipsis when truncated: %q", got)
	}

	text := strings.Repeat("中a", 101)
	line := `{"type":"item.completed","item":{"type":"agent_message","text":"` + text + `"}}`
	event := fmtEvent([]byte(line))
	if !utf8.ValidString(event) {
		t.Fatalf("fmtEvent returned invalid UTF-8: %q", event)
	}
	body := strings.TrimPrefix(event, "💬 ")
	if n := utf8.RuneCountInString(body); n != 201 {
		t.Fatalf("fmtEvent body rune count = %d, want 201", n)
	}
	if !strings.HasSuffix(body, "…") {
		t.Fatalf("fmtEvent body should append ellipsis when truncated: %q", body)
	}

	tail := tailRunes(mixed, 7)
	if !utf8.ValidString(tail) {
		t.Fatalf("tailRunes returned invalid UTF-8: %q", tail)
	}
	if n := utf8.RuneCountInString(tail); n != 7 {
		t.Fatalf("tailRunes rune count = %d, want 7", n)
	}
}

// round counter: new session always allowed; resume capped at max.
func TestRounds(t *testing.T) {
	c := &rounds{data: map[string]*entry{}, max: 2, ttl: time.Hour}

	if ok, _ := c.check(""); !ok {
		t.Fatal("new session must be allowed")
	}
	r, rem := c.commit("s1", "read-only") // round 1
	if r != 1 || rem != 1 {
		t.Fatalf("round=%d rem=%d, want 1,1", r, rem)
	}
	if ok, _ := c.check("s1"); !ok {
		t.Fatal("round 2 should be allowed (max 2)")
	}
	c.commit("s1", "read-only") // round 2
	if ok, _ := c.check("s1"); ok {
		t.Fatal("round 3 must be rejected (max 2)")
	}
}

func TestRoundsSandboxStoredAndPreserved(t *testing.T) {
	c := &rounds{data: map[string]*entry{}, max: 6, ttl: time.Hour}
	c.commit("s1", "workspace-write")
	c.commit("s1", "read-only")

	got, ok := c.sandboxOf("s1")
	if !ok {
		t.Fatal("sandbox should be stored")
	}
	if got != "workspace-write" {
		t.Fatalf("sandbox = %q, want workspace-write", got)
	}
}

func TestCommitSweepsExpiredSessions(t *testing.T) {
	c := &rounds{data: map[string]*entry{}, max: 6, ttl: time.Hour}
	c.data["old"] = &entry{count: 1, last: time.Now().Add(-2 * time.Hour), sandbox: "read-only"}
	c.commit("new", "read-only")

	if _, ok := c.data["old"]; ok {
		t.Fatal("commit should sweep expired sessions")
	}
}

// expired sessions get swept so the map can't grow forever.
func TestSweep(t *testing.T) {
	c := &rounds{data: map[string]*entry{}, max: 6, ttl: time.Hour}
	c.data["old"] = &entry{count: 1, last: time.Now().Add(-2 * time.Hour)}
	c.data["new"] = &entry{count: 1, last: time.Now()}
	c.sweep()
	if _, ok := c.data["old"]; ok {
		t.Error("expired session should be swept")
	}
	if _, ok := c.data["new"]; !ok {
		t.Error("fresh session should survive")
	}
}

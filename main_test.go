package main

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"
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

// round counter: new session always allowed; resume capped at max.
func TestRounds(t *testing.T) {
	c := &rounds{data: map[string]*entry{}, max: 2, ttl: time.Hour}

	if ok, _ := c.check(""); !ok {
		t.Fatal("new session must be allowed")
	}
	r, rem := c.commit("s1") // round 1
	if r != 1 || rem != 1 {
		t.Fatalf("round=%d rem=%d, want 1,1", r, rem)
	}
	if ok, _ := c.check("s1"); !ok {
		t.Fatal("round 2 should be allowed (max 2)")
	}
	c.commit("s1") // round 2
	if ok, _ := c.check("s1"); ok {
		t.Fatal("round 3 must be rejected (max 2)")
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

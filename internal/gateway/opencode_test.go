package gateway

import (
	"net/http/httptest"
	"testing"
)

func TestOpenCodeSession(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	first := openCodeSession(r, []byte(`{"input":[{"role":"developer","content":"system"},{"role":"user","content":"hello"}]}`))
	next := openCodeSession(r, []byte(`{"input":[{"role":"developer","content":"system"},{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"next"}]}`))
	if first == next || first == "gateway_auxiliary" {
		t.Fatalf("different full requests shared ID %s", first)
	}
	other := openCodeSession(r, []byte(`{"input":[{"role":"user","content":"different"}]}`))
	if first == other {
		t.Fatal("different conversations share ID")
	}
	if openCodeSession(r, []byte(`{"input":[{"role":"developer","content":"system"},{"role":"user","content":"hello"}]}`)) != first {
		t.Fatal("same full request changed ID")
	}
	r.Header.Set("x-opencode-session", "explicit-session")
	if openCodeSession(r, nil) != "explicit-session" {
		t.Fatal("explicit session lost")
	}
	r.Header.Set("x-session-id", "generic-session")
	r.Header.Set("X-Claude-Code-Session-Id", "claude-session")
	if openCodeSession(r, nil) != "claude-session" {
		t.Fatal("Claude Code session must take priority")
	}
	r.Header.Del("X-Claude-Code-Session-Id")
	if openCodeSession(r, nil) != "explicit-session" {
		t.Fatal("OpenCode fallback priority changed")
	}
	r.Header.Del("x-opencode-session")
	if openCodeSession(r, nil) != "generic-session" {
		t.Fatal("generic session fallback lost")
	}
}

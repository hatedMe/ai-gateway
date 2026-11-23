package translate

import (
	"bytes"
	"testing"
)

func FuzzSSE(f *testing.F) {
	for _, seed := range []string{"", "data: {}\n\n", "data: {\n", "data: 1\ndata: 2\n\n", ": heartbeat\r\n\r\n"} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 64<<10 {
			t.Skip()
		}
		_ = readSSE(bytes.NewReader(raw), func(frame []byte) (bool, error) { return false, nil })
	})
}

func FuzzRequestConversion(f *testing.F) {
	for _, seed := range []string{`{}`, `{"model":"m","max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`, `{"model":"m","max_tokens":32,"tools":[{"name":"f","input_schema":{"type":"object","enum":[9007199254740993]}}],"messages":[]}`, `{"model":"m","messages":[{"role":"tool","tool_call_id":"missing","content":"x"}]}`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 32<<10 {
			t.Skip()
		}
		_, _ = PrepareRequest(raw, RequestOptions{})
		_, _, _ = PrepareChatRequest(raw, RequestOptions{})
	})
}

package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChatReadableReasoningAliases(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		bad          bool
	}{
		{"alias", `"reasoning":"think"`, false},
		{"opencode-duplicate", `"reasoning":"think","reasoning_details":[{"type":"reasoning.text","text":"think","format":"unknown","index":0}]`, false},
		{"details-only", `"reasoning_details":[{"type":"reasoning.text","text":"think","format":"unknown","index":0}]`, false},
		{"canonical-duplicate", `"reasoning_content":"think","reasoning":"think"`, false},
		{"null-alias", `"reasoning_content":"think","reasoning":null,"reasoning_details":null`, false},
		{"conflict", `"reasoning_content":"think","reasoning":"other"`, true},
		{"details-conflict", `"reasoning":"think","reasoning_details":[{"type":"reasoning.text","text":"other"}]`, true},
		{"encrypted", `"reasoning_details":[{"type":"reasoning.encrypted","data":"private"}]`, true},
		{"signed", `"reasoning_details":[{"type":"reasoning.text","text":"think","signature":"private"}]`, true},
		{"invalid-alias", `"reasoning":{"text":"think"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{"id":"c","model":"m","choices":[{"message":{"role":"assistant","content":"OK",` + tc.fields + `},"finish_reason":"stop"}]}`
			result, err := DecodeUpstreamResponse(ChatProtocol, []byte(raw))
			if tc.bad {
				if err == nil {
					t.Fatal("unsupported or conflicting reasoning accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(result), `"text":"think"`) != 1 {
				t.Fatalf("readable summary lost or duplicated: %s", result)
			}
			if !strings.Contains(string(result), `"text":"OK"`) {
				t.Fatal("answer lost", string(result))
			}
		})
	}
}

func TestChatStreamReadableReasoningAliases(t *testing.T) {
	for _, fields := range []string{
		`"reasoning":"think"`,
		`"reasoning":"think","reasoning_details":[{"type":"reasoning.text","text":"think","format":"unknown","index":0}]`,
	} {
		var summaries strings.Builder
		completed := false
		source := chatFrames(streamChunk(`{`+fields+`}`), streamChunk(`{"reasoning":null,"content":"OK"}`), `{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, "[DONE]")
		err := DecodeUpstreamStream(ChatProtocol, strings.NewReader(source), func(name string, payload any) error {
			if name == "response.completed" {
				completed = true
			}
			if name == "response.reasoning_summary_text.delta" {
				raw, _ := json.Marshal(payload)
				var event struct{ Delta string }
				_ = json.Unmarshal(raw, &event)
				summaries.WriteString(event.Delta)
			}
			return nil
		})
		if err != nil || !completed || summaries.String() != "think" {
			t.Fatalf("summary=%q completed=%t err=%v", summaries.String(), completed, err)
		}
	}
}

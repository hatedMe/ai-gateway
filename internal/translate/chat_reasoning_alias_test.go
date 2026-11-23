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

func TestChatStreamRepeatedFinishWithUsage(t *testing.T) {
	for _, reason := range []string{"stop", "length"} {
		t.Run(reason, func(t *testing.T) {
			terminal := `{"id":"c","choices":[{"index":0,"delta":{"role":"assistant","content":"","reasoning":null},"finish_reason":"` + reason + `"}]}`
			tail := `{"id":"c","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":"` + reason + `"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`
			events, err := collectStream(t, chatFrames(streamChunk(`{"content":"OK"}`), terminal, tail, "[DONE]"))
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, event := range events {
				if event["type"] == "response.completed" || event["type"] == "response.incomplete" {
					count++
					response := event["response"].(map[string]any)
					usage := response["usage"].(map[string]any)
					if usage["input_tokens"] != float64(7) || usage["output_tokens"] != float64(3) {
						t.Fatal("usage tail lost", usage)
					}
				}
			}
			if count != 1 {
				t.Fatal("terminal count", count)
			}
			if _, err := collectStream(t, chatFrames(streamChunk(`{"content":"OK"}`), terminal, tail)); err == nil {
				t.Fatal("usage tail substituted for missing DONE")
			}
		})
	}
	for _, tail := range []string{
		`{"id":"c","choices":[{"index":0,"delta":{"content":"late"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`,
		`{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`,
		`{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	} {
		terminal := `{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
		if _, err := collectStream(t, chatFrames(streamChunk(`{"content":"OK"}`), terminal, tail, "[DONE]")); err == nil {
			t.Fatal("late content or invalid repeated finish accepted")
		}
	}
}

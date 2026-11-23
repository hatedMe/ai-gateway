package translate

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestReasoningSummaryParity(t *testing.T) {
	const summary = "先分析\n再核验"
	const answer = "结论"
	wantOutput := []any{
		map[string]any{"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": summary}}},
		map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": answer}}},
	}
	assertOutput := func(t *testing.T, response map[string]any) {
		t.Helper()
		output, ok := response["output"].([]any)
		if !ok || len(output) != 2 {
			t.Fatalf("reasoning summary and answer must both survive in order: %#v", response["output"])
		}
		// Generated IDs and status fields are independent of summary semantics.
		semantic := make([]any, len(output))
		for i, value := range output {
			item, ok := value.(map[string]any)
			if !ok {
				t.Fatalf("invalid output item: %#v", value)
			}
			copy := map[string]any{}
			for key, value := range item {
				if key != "id" && key != "status" {
					copy[key] = value
				}
			}
			semantic[i] = copy
		}
		if !reflect.DeepEqual(semantic, wantOutput) {
			t.Fatalf("summary/answer content or order changed: %#v", semantic)
		}
	}
	t.Run("JSON", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{"id": "chat-summary", "model": "real", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "reasoning_content": summary, "content": answer}, "finish_reason": "stop"}}})
		response, err := DecodeUpstreamResponse(ChatProtocol, raw)
		if err != nil {
			t.Fatal(err)
		}
		var root map[string]any
		if err := json.Unmarshal(response, &root); err != nil {
			t.Fatal(err)
		}
		assertOutput(t, root)
		messages, err := ConvertResponseForChat(response, "public")
		if err != nil {
			t.Fatal(err)
		}
		chat, err := ChatResponse(messages)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Choices []struct {
				Message struct {
					Reasoning string `json:"reasoning_content"`
					Content   string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(chat, &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Choices) != 1 || result.Choices[0].Message.Reasoning != summary || result.Choices[0].Message.Content != answer {
			t.Fatalf("Chat JSON round trip lost summary: %s", chat)
		}
	})
	t.Run("SSE", func(t *testing.T) {
		var source strings.Builder
		for _, delta := range []map[string]any{{"role": "assistant", "reasoning_content": "先分析\n"}, {"reasoning_content": "再核验"}, {"content": answer}} {
			chunk, _ := json.Marshal(map[string]any{"id": "chat-summary", "model": "real", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}})
			fmt.Fprintf(&source, "data: %s\n\n", chunk)
		}
		source.WriteString("data: {\"id\":\"chat-summary\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		var normalized strings.Builder
		var summaryDelta, answerDelta strings.Builder
		var terminal map[string]any
		seenAnswer := false
		err := DecodeUpstreamStream(ChatProtocol, strings.NewReader(source.String()), func(name string, payload any) error {
			raw, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			fmt.Fprintf(&normalized, "event: %s\ndata: %s\n\n", name, raw)
			var event map[string]any
			if err := json.Unmarshal(raw, &event); err != nil {
				return err
			}
			switch name {
			case "response.reasoning_summary_text.delta":
				if seenAnswer {
					t.Error("summary delta moved after answer")
				}
				text, _ := event["delta"].(string)
				summaryDelta.WriteString(text)
			case "response.output_text.delta":
				seenAnswer = true
				text, _ := event["delta"].(string)
				answerDelta.WriteString(text)
			case "response.completed":
				terminal, _ = event["response"].(map[string]any)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if summaryDelta.String() != summary || answerDelta.String() != answer {
			t.Fatalf("SSE deltas lost content: summary=%q answer=%q", summaryDelta.String(), answerDelta.String())
		}
		assertOutput(t, terminal)
		var returnedSummary, returnedAnswer strings.Builder
		seenAnswer = false
		bridge := NewChatStream("public", false, func(value any) error {
			if value == nil {
				return nil
			}
			raw, err := json.Marshal(value)
			if err != nil {
				return err
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						Reasoning string `json:"reasoning_content"`
						Content   string `json:"content"`
					} `json:"delta"`
				} `json:"choices"`
			}
			if err := json.Unmarshal(raw, &chunk); err != nil {
				return err
			}
			for _, choice := range chunk.Choices {
				if choice.Delta.Reasoning != "" && seenAnswer {
					t.Error("Chat summary moved after answer")
				}
				returnedSummary.WriteString(choice.Delta.Reasoning)
				if choice.Delta.Content != "" {
					seenAnswer = true
				}
				returnedAnswer.WriteString(choice.Delta.Content)
			}
			return nil
		})
		if err := ConvertStreamForChat(strings.NewReader(normalized.String()), "public", bridge.Emit); err != nil {
			t.Fatal(err)
		}
		if returnedSummary.String() != summary || returnedAnswer.String() != answer {
			t.Fatalf("Chat SSE round trip differs: summary=%q answer=%q", returnedSummary.String(), returnedAnswer.String())
		}
	})
}

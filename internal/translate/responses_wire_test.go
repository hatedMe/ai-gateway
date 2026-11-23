package translate

import (
	"encoding/json"
	"testing"
)

// Responses clients validate the wire format, including fields our internal
// message representation does not consume.
func TestConvertedResponsesWireFields(t *testing.T) {
	for protocol, source := range map[Protocol]string{
		ChatProtocol:      `{"id":"chat1","created":1700000000,"model":"m","choices":[{"message":{"role":"assistant","reasoning_content":"summary","content":"answer","tool_calls":[{"id":"call1","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
		AnthropicProtocol: `{"id":"a","model":"m","content":[{"type":"text","text":"answer"},{"type":"tool_use","id":"call1","name":"f","input":{}}],"stop_reason":"tool_use"}`,
	} {
		t.Run(string(protocol), func(t *testing.T) {
			body, err := DecodeUpstreamResponse(protocol, []byte(source))
			if err != nil {
				t.Fatal(err)
			}
			var root map[string]any
			_ = json.Unmarshal(body, &root)
			if created, ok := root["created_at"].(float64); !ok || created <= 0 {
				t.Error("missing response creation time")
			}
			if protocol == ChatProtocol && root["created_at"] != float64(1700000000) {
				t.Error("upstream creation time was lost")
			}
			seen := map[string]bool{}
			for _, value := range root["output"].([]any) {
				item := value.(map[string]any)
				id, ok := item["id"].(string)
				if !ok || id == "" || seen[id] {
					t.Error("missing or duplicate output item id")
				}
				seen[id] = true
				if item["status"] != "completed" {
					t.Error("missing output item status")
				}
				if item["type"] == "message" {
					for _, part := range item["content"].([]any) {
						block := part.(map[string]any)
						if block["type"] == "output_text" {
							if _, ok := block["annotations"].([]any); !ok {
								t.Error("missing text annotations array")
							}
						}
					}
				}
			}
		})
	}
}

func TestConvertedResponsesStreamWireFields(t *testing.T) {
	events, err := collectStream(t, chatFrames(streamChunk(`{"reasoning_content":"summary"}`), streamChunk(`{"content":"answer"}`), chatTerminalFixture, "[DONE]"))
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		switch event["type"] {
		case "response.created", "response.completed":
			response := event["response"].(map[string]any)
			if _, ok := response["created_at"].(float64); !ok {
				t.Error("missing streamed response creation time")
			}
		case "response.content_part.added", "response.content_part.done", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
			if id, ok := event["item_id"].(string); !ok || id == "" {
				t.Errorf("missing item_id on %s", event["type"])
			}
		}
	}
}

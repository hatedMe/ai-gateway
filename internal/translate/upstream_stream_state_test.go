package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

func chatFrames(chunks ...string) string {
	var out strings.Builder
	for _, chunk := range chunks {
		out.WriteString("data: " + chunk + "\n\n")
	}
	return out.String()
}
func streamChunk(delta string) string {
	return `{"id":"c","model":"m","choices":[{"index":0,"delta":` + delta + `,"finish_reason":null}]}`
}
func collectStream(t *testing.T, frames string) ([]object, error) {
	t.Helper()
	var events []object
	err := DecodeUpstreamStream(ChatProtocol, strings.NewReader(frames), func(_ string, v any) error {
		raw, _ := json.Marshal(v)
		var event object
		_ = json.Unmarshal(raw, &event)
		events = append(events, event)
		return nil
	})
	return events, err
}

const chatTerminalFixture = `{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`

func TestStreamLateToolIdentity(t *testing.T) {
	_, lateErr := collectStream(t, chatFrames(streamChunk(`{"tool_calls":[{"index":0,"id":"a"}]}`), streamChunk(`{"tool_calls":[{"index":0,"function":{"name":"first","arguments":"{}"}}]}`), chatTerminalFixture, "[DONE]"))
	if lateErr != nil {
		t.Fatal("identity-only delta must be buffered", lateErr)
	}
	frames := chatFrames(streamChunk(`{"tool_calls":[{"index":0,"function":{"arguments":"{"}},{"index":1,"id":"b","function":{"name":"second","arguments":"{}"}}]}`), streamChunk(`{"tool_calls":[{"index":0,"id":"a","function":{"name":"first","arguments":"}"}}]}`), chatTerminalFixture, "[DONE]")
	events, err := collectStream(t, frames)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range events {
		if e["type"] == "response.output_item.added" {
			names = append(names, e["item"].(map[string]any)["name"].(string))
		}
	}
	if strings.Join(names, ",") != "first,second" {
		t.Fatal(names)
	}
	_, err = collectStream(t, chatFrames(streamChunk(`{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}`), chatTerminalFixture, "[DONE]"))
	if err == nil {
		t.Fatal("accepted missing identity")
	}
}

func TestStreamInterleavedSegments(t *testing.T) {
	events, err := collectStream(t, chatFrames(streamChunk(`{"content":"before"}`), streamChunk(`{"tool_calls":[{"index":0,"id":"a","function":{"name":"f","arguments":"{}"}}]}`), streamChunk(`{"content":"after"}`), chatTerminalFixture, "[DONE]"))
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]["response"].(map[string]any)["output"].([]any)
	if len(last) != 3 || last[1].(map[string]any)["type"] != "function_call" {
		t.Fatal(last)
	}
}

func TestStreamRefusalParity(t *testing.T) {
	events, err := collectStream(t, chatFrames(streamChunk(`{"refusal":"cannot"}`), streamChunk(`{"refusal":" comply"}`), chatTerminalFixture, "[DONE]"))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range events {
		if e["type"] == "response.refusal.done" {
			count++
			if e["refusal"] != "cannot comply" {
				t.Fatal(e)
			}
		}
	}
	if count != 1 {
		t.Fatal(events)
	}
}

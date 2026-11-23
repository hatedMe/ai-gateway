package translate

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

type capturedEvent struct {
	Name string
	Body map[string]any
}

func runStream(t *testing.T, raw string) ([]capturedEvent, error) {
	t.Helper()
	events := []capturedEvent{}
	err := ConvertStream(strings.NewReader(raw), "claude-alias", func(name string, v any) error {
		b, e := json.Marshal(v)
		if e != nil {
			return e
		}
		events = append(events, capturedEvent{name, decode(t, b)})
		return nil
	})
	return events, err
}

func frame(raw string) string { return "data: " + raw + "\n\n" }

func toolStream() string {
	events := []string{
		`{"type":"response.created","response":{"id":"resp_1","status":"in_progress","model":"gpt-test","output":[]}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`,
		`{"type":"response.content_part.added","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`,
		`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"check"}`,
		`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"ing"}`,
		`{"type":"response.content_part.done","output_index":0,"content_index":0,"part":{"type":"output_text","text":"checking"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"checking"}]}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"weather","arguments":""}}`,
		`{"type":"response.output_item.added","output_index":2,"item":{"id":"fc_2","type":"function_call","call_id":"call_2","name":"weather","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"city\":"}`,
		`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"{}"}`,
		`{"type":"response.function_call_arguments.done","output_index":2,"arguments":"{}"}`,
		`{"type":"response.output_item.done","output_index":2,"item":{"id":"fc_2","type":"function_call","call_id":"call_2","name":"weather","arguments":"{}"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"\"上海\"}"}`,
		`{"type":"response.function_call_arguments.done","output_index":1,"arguments":"{\"city\":\"上海\"}"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"weather","arguments":"{\"city\":\"上海\"}"}}`,
		`{"type":"response.completed","response":` + toolResponse + `}`,
	}
	var s strings.Builder
	s.WriteString(": heartbeat\r\n\r\n")
	for _, e := range events {
		s.WriteString(frame(e))
	}
	return s.String()
}

func TestStreamInterleavedToolsAndMatchesNonStream(t *testing.T) {
	events, err := runStream(t, toolStream())
	if err != nil {
		t.Fatal(err)
	}
	if events[0].Name != "message_start" || events[len(events)-1].Name != "message_stop" {
		t.Fatal(events)
	}
	blocks := []any{}
	active := -1
	args := ""
	for _, e := range events {
		switch e.Name {
		case "content_block_start":
			if active != -1 {
				t.Fatal("overlapping Anthropic blocks")
			}
			active = int(e.Body["index"].(float64))
			args = ""
			blocks = append(blocks, e.Body["content_block"])
		case "content_block_delta":
			if active != int(e.Body["index"].(float64)) {
				t.Fatal("wrong active block")
			}
			d := e.Body["delta"].(map[string]any)
			b := blocks[active].(map[string]any)
			if d["type"] == "text_delta" {
				b["text"] = b["text"].(string) + d["text"].(string)
			} else {
				args += d["partial_json"].(string)
			}
		case "content_block_stop":
			if blocks[active].(map[string]any)["type"] == "tool_use" {
				blocks[active].(map[string]any)["input"] = decode(t, []byte(args))
			}
			active = -1
		}
	}
	b, e := ConvertResponse([]byte(toolResponse), "claude-alias")
	if e != nil {
		t.Fatal(e)
	}
	normal := decode(t, b)
	if !reflect.DeepEqual(normal["content"], blocks) {
		t.Fatalf("stream=%v normal=%v", blocks, normal["content"])
	}
	last := events[len(events)-2].Body
	if !reflect.DeepEqual(last["usage"], normal["usage"]) || last["delta"].(map[string]any)["stop_reason"] != normal["stop_reason"] {
		t.Fatal(last)
	}
}

func TestStreamTerminalOnlyAndLargeMultilineFrames(t *testing.T) {
	// A terminal snapshot can fill content absent from incremental events.
	raw := "data: {\"type\":\"response.completed\",\n" + "data: \"response\":" + toolResponse + "}\r\n\r\n"
	events, err := runStream(t, raw)
	if err != nil || len(events) == 0 {
		t.Fatal(err)
	}
	large := strings.Repeat("x", 100000)
	snapshot := fmt.Sprintf(`{"id":"r","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":%q}]}]}`, large)
	_, err = runStream(t, frame(`{"type":"response.completed","response":`+snapshot+`}`))
	if err != nil {
		t.Fatal(err)
	}
}

func TestStreamErrorsNeverEmitMessageStop(t *testing.T) {
	created := frame(`{"type":"response.created","response":{"id":"r","status":"in_progress"}}`)
	for _, raw := range []string{
		"", created, created + frame(`{"type":"response.failed","response":{"id":"r","status":"failed","error":{"message":"bad"}}}`),
		created + frame(`{"type":"error","message":"upstream failed"}`),
		created + frame(`{"type":"response.output_text.delta","output_index":9,"delta":"orphan"}`),
		created + frame(`{"type":"response.completed","response":{"id":"r","status":"completed","output":[{"type":"web_search_call"}]}}`),
		created + "data: not-json\n\n",
		created + "data: [DONE]\n\n",
	} {
		events, err := runStream(t, raw)
		if err == nil {
			t.Fatalf("accepted truncated/invalid stream %s", raw)
		}
		for _, e := range events {
			if e.Name == "message_stop" {
				t.Fatal("false success")
			}
		}
	}
}

func TestStreamIncomplete(t *testing.T) {
	events, err := runStream(t, frame(`{"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if events[len(events)-2].Body["delta"].(map[string]any)["stop_reason"] != "max_tokens" {
		t.Fatal(events)
	}
}

func TestStreamWriteError(t *testing.T) {
	err := ConvertStream(strings.NewReader(toolStream()), "m", func(string, any) error { return io.ErrClosedPipe })
	if err != io.ErrClosedPipe {
		t.Fatal(err)
	}
}

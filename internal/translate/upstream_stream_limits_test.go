package translate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestStreamLateIdentityMatrix(t *testing.T) {
	for _, tc := range []struct {
		name   string
		deltas []string
		bad    bool
	}{
		{"ID-last", []string{`{"tool_calls":[{"index":0,"function":{"name":"lookup","arguments":"{"}}]}`, `{"tool_calls":[{"index":0,"id":"call-a","function":{"arguments":"}"}}]}`}, false},
		{"name-last", []string{`{"tool_calls":[{"index":0,"id":"call-a","function":{"arguments":"{"}}]}`, `{"tool_calls":[{"index":0,"function":{"name":"lookup","arguments":"}"}}]}`}, false},
		{"both-separated", []string{`{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}`, `{"tool_calls":[{"index":0,"id":"call-a","function":{}}]}`, `{"tool_calls":[{"index":0,"function":{"name":"lookup"}}]}`}, false},
		{"same-identity", []string{`{"tool_calls":[{"index":0,"id":"call-a","function":{"name":"lookup","arguments":"{"}}]}`, `{"tool_calls":[{"index":0,"id":"call-a","function":{"name":"lookup","arguments":"}"}}]}`}, false},
		{"conflicting-ID", []string{`{"tool_calls":[{"index":0,"id":"call-a","function":{"name":"lookup","arguments":"{}"}}]}`, `{"tool_calls":[{"index":0,"id":"other","function":{}}]}`}, true},
		{"conflicting-name", []string{`{"tool_calls":[{"index":0,"id":"call-a","function":{"name":"lookup","arguments":"{}"}}]}`, `{"tool_calls":[{"index":0,"function":{"name":"other"}}]}`}, true},
		{"missing-ID", []string{`{"tool_calls":[{"index":0,"function":{"name":"lookup","arguments":"{}"}}]}`}, true},
		{"missing-name", []string{`{"tool_calls":[{"index":0,"id":"call-a","function":{"arguments":"{}"}}]}`}, true},
		{"missing-both", []string{`{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}`}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var chunks []string
			for _, delta := range tc.deltas {
				chunks = append(chunks, streamChunk(delta))
			}
			chunks = append(chunks, chatTerminalFixture, "[DONE]")
			events, err := collectStream(t, chatFrames(chunks...))
			if tc.bad {
				var ce *ConversionError
				if !errors.As(err, &ce) || ce.Code != "invalid_tool_identity" {
					t.Fatalf("expected identity failure, got %v", err)
				}
				assertNoStreamSuccess(t, events)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			response := events[len(events)-1]["response"].(map[string]any)
			output := response["output"].([]any)
			if len(output) != 1 {
				t.Fatalf("duplicate tool item: %#v", output)
			}
			item := output[0].(map[string]any)
			if item["call_id"] != "call-a" || item["name"] != "lookup" || item["arguments"] != "{}" {
				t.Fatalf("identity or buffered arguments lost: %#v", item)
			}
			assertStreamOnceAndSequence(t, events)
		})
	}
}

func TestStreamPendingIdentityBlocksLaterOutput(t *testing.T) {
	var added []string
	s := normalizedStream{id: "c", keys: map[int]int{}, closed: map[int]bool{}, emit: func(name string, value any) error {
		if name == "response.output_item.added" {
			item := value.(object)["item"].(object)
			if item["type"] == "function_call" {
				added = append(added, item["name"].(string))
			} else {
				added = append(added, "text")
			}
		}
		return nil
	}}
	c := chatSourceState{}
	call := func(index int, id, name, args string) error {
		fn := object{"arguments": args}
		tool := object{"index": json.Number(fmt.Sprint(index)), "function": fn}
		if id != "" {
			tool["id"] = id
		}
		if name != "" {
			fn["name"] = name
		}
		return c.tool(&s, tool)
	}
	if err := call(0, "", "first", "{"); err != nil {
		t.Fatal(err)
	}
	if err := call(1, "b", "second", "{}"); err != nil {
		t.Fatal(err)
	}
	if err := c.content(&s, "after", "text"); err != nil {
		t.Fatal(err)
	}
	if len(added) != 0 || len(s.output) != 0 {
		t.Fatal("later tool/text escaped blocked first identity", added)
	}
	if err := call(0, "a", "", "}"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(added, []string{"first", "second", "text"}) {
		t.Fatalf("source order changed: %v", added)
	}
	if c.pendingBytes != 0 || len(c.pending) != 0 {
		t.Fatalf("flushed content retained: %+v", c)
	}
}

func assertNoStreamSuccess(t *testing.T, events []object) {
	t.Helper()
	for _, event := range events {
		if event["type"] == "response.completed" || event["type"] == "response.function_call_arguments.done" || event["type"] == "response.output_item.done" {
			t.Fatalf("failed stream emitted successful done event: %#v", event)
		}
	}
}

func assertStreamOnceAndSequence(t *testing.T, events []object) {
	t.Helper()
	seen := map[string]bool{}
	previous := -1.0
	terminals := 0
	for _, event := range events {
		sequence, ok := event["sequence_number"].(float64)
		if !ok || sequence <= previous {
			t.Fatalf("sequence did not increase: previous=%v event=%#v", previous, event)
		}
		previous = sequence
		name, _ := event["type"].(string)
		if name == "response.completed" || name == "response.incomplete" {
			terminals++
		}
		if strings.HasSuffix(name, ".done") {
			key := fmt.Sprint(name, ":", event["output_index"], ":", event["content_index"], ":", event["summary_index"])
			if seen[key] {
				t.Fatalf("duplicate done event: %#v", event)
			}
			seen[key] = true
		}
	}
	if terminals != 1 {
		t.Fatalf("expected one terminal response, got %d", terminals)
	}
}

func TestStreamFinishUsageAndOnceContract(t *testing.T) {
	frames := chatFrames(streamChunk(`{"content":"before"}`), streamChunk(`{"tool_calls":[{"index":0,"id":"a","function":{"name":"lookup","arguments":"{}"}}]}`), streamChunk(`{"content":"after"}`), chatTerminalFixture, `{"id":"c","choices":[],"usage":{"prompt_tokens":9,"completion_tokens":4}}`, `{"id":"c","choices":[],"usage":{"prompt_tokens":9,"completion_tokens":4}}`, "[DONE]")
	events, err := collectStream(t, frames)
	if err != nil {
		t.Fatal(err)
	}
	assertStreamOnceAndSequence(t, events)
	response := events[len(events)-1]["response"].(map[string]any)
	usage := response["usage"].(map[string]any)
	if usage["input_tokens"] != float64(9) || usage["output_tokens"] != float64(4) {
		t.Fatalf("tail usage lost: %#v", usage)
	}
	counts := map[string]int{}
	for _, event := range events {
		name := event["type"].(string)
		counts[name]++
	}
	for name, want := range map[string]int{"response.output_text.done": 2, "response.function_call_arguments.done": 1, "response.output_item.done": 3, "response.completed": 1} {
		if counts[name] != want {
			t.Errorf("%s count=%d want=%d", name, counts[name], want)
		}
	}
}

func TestStreamFailureAndFilterBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, frames string
		eof, filter  bool
	}{
		{"EOF-before-finish", chatFrames(streamChunk(`{"content":"partial"}`)), true, false},
		{"EOF-after-finish", chatFrames(streamChunk(`{"content":"partial"}`), chatTerminalFixture), true, false},
		{"explicit-error", chatFrames(streamChunk(`{"content":"partial"}`), `{"error":{"message":"SECRET_SENTINEL"}}`), false, false},
		{"truncated-arguments", chatFrames(streamChunk(`{"tool_calls":[{"index":0,"id":"a","function":{"name":"lookup","arguments":"{"}}]}`), `{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`, "[DONE]"), false, false},
		{"filter", chatFrames(streamChunk(`{"content":"partial"}`), `{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}]}`, "[DONE]"), false, true},
		{"delta-after-finish", chatFrames(chatTerminalFixture, streamChunk(`{"content":"late"}`), "[DONE]"), false, false},
		{"duplicate-finish", chatFrames(chatTerminalFixture, chatTerminalFixture, "[DONE]"), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events, err := collectStream(t, tc.frames)
			if tc.filter {
				if err != nil {
					t.Fatal(err)
				}
				assertStreamOnceAndSequence(t, events)
				last := events[len(events)-1]
				if last["type"] != "response.incomplete" {
					t.Fatalf("filter became success: %#v", last)
				}
				r := last["response"].(map[string]any)
				if r["incomplete_details"].(map[string]any)["reason"] != "content_filter" {
					t.Fatal(r)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid stream accepted")
			}
			if tc.eof && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("expected EOF failure, got %v", err)
			}
			if strings.Contains(err.Error(), "SECRET_SENTINEL") {
				t.Fatal("upstream error content leaked")
			}
			assertNoStreamSuccess(t, events)
		})
	}
}

// Reads one byte at a time to split UTF-8 codepoints and prevent a buffered
// reader's legitimate read-ahead from obscuring reads after emitter failure.
type streamBoundaryReader struct {
	source         string
	offset         int
	stopped        *bool
	readsAfterStop int
}

func (r *streamBoundaryReader) Read(p []byte) (int, error) {
	if r.stopped != nil && *r.stopped {
		r.readsAfterStop++
		return 0, errors.New("read after emitter failure")
	}
	if r.offset == len(r.source) {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = r.source[r.offset]
	r.offset++
	return 1, nil
}

func TestStreamUTF8MultilineFraming(t *testing.T) {
	source := ": heartbeat\r\nevent: chunk\r\ndata: {\"id\":\"c\",\"choices\":[\r\ndata: {\"index\":0,\"delta\":{\"content\":\"中文🙂\\n下一行\"},\"finish_reason\":null}]}\r\n\r\n" + chatFrames(chatTerminalFixture, "[DONE]")
	var text strings.Builder
	err := DecodeUpstreamStream(ChatProtocol, &streamBoundaryReader{source: source}, func(name string, value any) error {
		if name == "response.output_text.delta" {
			text.WriteString(value.(object)["delta"].(string))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if text.String() != "中文🙂\n下一行" {
		t.Fatalf("UTF-8/multiline frame changed business text: %q", text.String())
	}
}

func TestStreamEmitterErrorStopsReading(t *testing.T) {
	sentinel := errors.New("client disconnected")
	stopped := false
	reader := &streamBoundaryReader{source: chatFrames(streamChunk(`{"content":"first"}`), streamChunk(`{"content":"unread"}`), chatTerminalFixture, "[DONE]"), stopped: &stopped}
	callbacks := 0
	err := DecodeUpstreamStream(ChatProtocol, reader, func(name string, _ any) error {
		callbacks++
		if name == "response.output_text.delta" {
			stopped = true
			return sentinel
		}
		return nil
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("emitter error lost: %v", err)
	}
	if reader.readsAfterStop != 0 || !stopped || reader.offset >= len(reader.source) {
		t.Fatalf("reader continued after failure: %+v", reader)
	}
	if callbacks != 4 {
		t.Fatalf("unexpected events after emitter failed: %d", callbacks)
	}
}

func TestStreamIdentityAndItemResourceLimits(t *testing.T) {
	const maxBytes = 32 << 20
	newState := func() normalizedStream {
		return normalizedStream{id: "c", keys: map[int]int{}, closed: map[int]bool{}, emit: func(string, any) error { return nil }}
	}
	t.Run("identity-counts-once", func(t *testing.T) {
		s := newState()
		s.bytes = maxBytes - 2
		c := chatSourceState{}
		call := object{"index": json.Number("0"), "id": "ab", "function": object{}}
		if err := c.tool(&s, call); err != nil {
			t.Fatal("exact byte limit rejected", err)
		}
		if err := c.tool(&s, call); err != nil || s.bytes != maxBytes {
			t.Fatalf("repeated identity double-counted: bytes=%d err=%v", s.bytes, err)
		}
		if err := c.tool(&s, object{"index": json.Number("0"), "function": object{"name": "f"}}); err == nil {
			t.Fatal("name bypassed cumulative identity byte limit")
		}
	})
	t.Run("pending-plus-identity", func(t *testing.T) {
		s := newState()
		s.bytes = maxBytes - 4
		c := chatSourceState{pendingBytes: 2}
		if err := c.tool(&s, object{"index": json.Number("0"), "id": "abc", "function": object{}}); err == nil {
			t.Fatal("pending bytes excluded from identity limit")
		}
	})
	t.Run("pending-count", func(t *testing.T) {
		s := newState()
		c := chatSourceState{tools: map[int]*toolIdentity{0: {}}, pending: make([]streamOperation, 32767)}
		for i := range c.pending {
			c.pending[i] = streamOperation{key: 0, kind: "tool"}
		}
		if err := c.enqueue(&s, streamOperation{key: 0, kind: "tool"}); err != nil {
			t.Fatal("exact pending limit rejected", err)
		}
		if err := c.enqueue(&s, streamOperation{key: 0, kind: "tool"}); err == nil {
			t.Fatal("pending item limit bypassed")
		}
	})
	t.Run("output-count", func(t *testing.T) {
		s := newState()
		s.output = make([]object, 32767)
		item := object{"type": "message", "content": []object{{"type": "output_text", "text": ""}}}
		if _, err := s.add(-1, item); err != nil {
			t.Fatal("exact output limit rejected", err)
		}
		if _, err := s.add(-2, item); err == nil {
			t.Fatal("output item limit bypassed")
		}
	})
	t.Run("pending-content-bytes", func(t *testing.T) {
		s := newState()
		s.bytes = maxBytes - 1
		c := chatSourceState{tools: map[int]*toolIdentity{0: {}}}
		if err := c.enqueue(&s, streamOperation{key: 0, kind: "tool", value: "a"}); err != nil {
			t.Fatal("exact pending content limit rejected", err)
		}
		if err := c.enqueue(&s, streamOperation{key: 0, kind: "tool", value: "b"}); err == nil {
			t.Fatal("pending bytes bypassed total byte limit")
		}
	})
}

func TestGeneratedStreamIDsCountAgainstBudget(t *testing.T) {
	s := normalizedStream{keys: map[int]int{}, closed: map[int]bool{}, emit: func(string, any) error { return nil }}
	if err := s.start(strings.Repeat("x", 1<<20), "model"); err != nil {
		t.Fatal(err)
	}
	c := chatSourceState{}
	for i := 0; i < 34; i++ {
		kind := "text"
		if i%2 == 1 {
			kind = "reasoning"
		}
		if err := c.content(&s, "a", kind); err != nil {
			if !strings.Contains(err.Error(), "limit") {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("generated IDs bypassed 32 MiB state budget")
}

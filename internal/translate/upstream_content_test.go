package translate

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCrossProtocolImageContent(t *testing.T) {
	contents := map[Protocol]string{
		ChatProtocol:      `[{"type":"text","text":"before"},{"type":"image_url","image_url":{"url":"https://example.test/a.png"}},{"type":"text","text":"between"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]`,
		ResponsesProtocol: `[{"type":"input_text","text":"before"},{"type":"input_image","image_url":"https://example.test/a.png"},{"type":"input_text","text":"between"},{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="}]`,
		AnthropicProtocol: `[{"type":"text","text":"before"},{"type":"image","source":{"type":"url","url":"https://example.test/a.png"}},{"type":"text","text":"between"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]`,
	}
	for source, input := range contents {
		for target, want := range contents {
			t.Run(string(source)+"/"+string(target), func(t *testing.T) {
				got, err := ConvertInputContent([]byte(input), source, target, true)
				if err != nil {
					t.Fatal(err)
				}
				assertContentJSON(t, got, want)
				var parts []json.RawMessage
				if err := json.Unmarshal([]byte(input), &parts); err != nil {
					t.Fatal(err)
				}
				got, err = ConvertInputContent([]byte("["+string(parts[1])+"]"), source, target, true)
				if err != nil {
					t.Fatal(err)
				}
				var wantParts []json.RawMessage
				if err := json.Unmarshal([]byte(want), &wantParts); err != nil {
					t.Fatal(err)
				}
				assertContentJSON(t, got, "["+string(wantParts[1])+"]")
			})
		}
	}
}

func TestCrossProtocolContentRefusal(t *testing.T) {
	cases := []struct {
		name   string
		source Protocol
		input  string
		allow  bool
		code   string
	}{
		{"disabled", ChatProtocol, `[{"type":"image_url","image_url":{"url":"https://example.test/secret"}}]`, false, "unsupported_capability"},
		{"invalid-base64", ResponsesProtocol, `[{"type":"input_image","image_url":"data:image/png;base64,secret!"}]`, true, "unsupported_field"},
		{"empty-base64", ResponsesProtocol, `[{"type":"input_image","image_url":"data:image/png;base64,"}]`, true, "unsupported_field"},
		{"pdf-data", ResponsesProtocol, `[{"type":"input_image","image_url":"data:application/pdf;base64,aGVsbG8="}]`, true, "unsupported_field"},
		{"svg-data", ResponsesProtocol, `[{"type":"input_image","image_url":"data:image/svg+xml;base64,aGVsbG8="}]`, true, "unsupported_field"},
		{"file-id", ResponsesProtocol, `[{"type":"input_image","file_id":"secret"}]`, true, "unsupported_field"},
		{"mixed-file", ResponsesProtocol, `[{"type":"input_text","text":"secret"},{"type":"input_file","file_id":"secret"}]`, true, "unsupported_field"},
		{"audio", ChatProtocol, `[{"type":"input_audio","input_audio":{"data":"secret"}}]`, true, "unsupported_field"},
		{"video", ChatProtocol, `[{"type":"video_url","video_url":"secret"}]`, true, "unsupported_field"},
		{"document", AnthropicProtocol, `[{"type":"document","source":{"type":"url","url":"secret"}}]`, true, "unsupported_field"},
		{"bad-url", ChatProtocol, `[{"type":"image_url","image_url":{"url":"file:///secret"}}]`, true, "unsupported_field"},
		{"bad-host", ChatProtocol, `[{"type":"image_url","image_url":{"url":"https:///secret"}}]`, true, "unsupported_field"},
		{"bad-source", AnthropicProtocol, `[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"secret!"}}]`, true, "unsupported_field"},
		{"extra-field", AnthropicProtocol, `[{"type":"text","text":"secret","citations":[1]}]`, true, "unsupported_field"},
		{"invalid-json", ChatProtocol, `[secret`, true, "unsupported_field"},
		{"null", ChatProtocol, `null`, true, "unsupported_field"},
		{"null-part", ChatProtocol, `[null]`, true, "unsupported_field"},
		{"empty", ChatProtocol, `[]`, true, "unsupported_field"},
		{"invalid-text", ChatProtocol, `[{"type":"text","text":null}]`, true, "unsupported_field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ConvertInputContent([]byte(tc.input), tc.source, AnthropicProtocol, tc.allow)
			var ce *ConversionError
			if !errors.As(err, &ce) {
				t.Fatalf("expected ConversionError, got %s, %v", got, err)
			}
			if ce.Code != tc.code || ce.Stage != "request_content" || ce.Field == "" {
				t.Fatalf("unsafe diagnostics: %#v", ce)
			}
			if strings.Contains(err.Error(), "secret") || got != nil {
				t.Fatalf("payload leaked or partial result: %s %v", got, err)
			}
		})
	}
}

func TestCrossProtocolContentTextAndDetail(t *testing.T) {
	got, err := ConvertInputContent([]byte(`[{"type":"output_text","text":"hello"}]`), ResponsesProtocol, ChatProtocol, false)
	if err != nil {
		t.Fatal(err)
	}
	assertContentJSON(t, got, `[{"type":"text","text":"hello"}]`)
	got, err = ConvertInputContent([]byte(`[{"type":"input_image","image_url":"http://example.test/i","detail":"high"}]`), ResponsesProtocol, ChatProtocol, true)
	if err != nil {
		t.Fatal(err)
	}
	assertContentJSON(t, got, `[{"type":"image_url","image_url":{"url":"http://example.test/i","detail":"high"}}]`)
	_, err = ConvertInputContent(got, ChatProtocol, AnthropicProtocol, true)
	if err == nil {
		t.Fatal("must not discard detail unsupported by Anthropic")
	}
	for _, pair := range [][2]Protocol{{"unknown", ChatProtocol}, {ChatProtocol, "unknown"}} {
		if _, err := ConvertInputContent([]byte(`[{"type":"text","text":"hello"}]`), pair[0], pair[1], false); err == nil {
			t.Fatal("unknown protocol accepted")
		}
	}
}

func TestCrossProtocolImageDoesNotDownload(t *testing.T) {
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	input, err := json.Marshal([]object{{"type": "input_image", "image_url": server.URL + "/image.png"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []Protocol{ChatProtocol, ResponsesProtocol, AnthropicProtocol} {
		if _, err := ConvertInputContent(input, ResponsesProtocol, target, true); err != nil {
			t.Fatal(err)
		}
	}
	if downloads.Load() != 0 {
		t.Fatal("content conversion downloaded a remote image")
	}
}

func assertContentJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("got %s, want %s", got, want)
	}
}

package gateway

import (
	"ai-gateway-go/internal/translate"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type requestEncodingError struct{ error }

func normalizeUpstreamResponse(resp *http.Response, protocol translate.Protocol, publicModel string) (*http.Response, error) {
	copied := *resp
	copied.Header = resp.Header.Clone()
	copied.Header.Del("Content-Length")
	copied.ContentLength = -1
	if strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		reader, writer := io.Pipe()
		copied.Body = reader
		go func() {
			defer resp.Body.Close()
			emit := func(name string, payload any) error {
				if publicModel != "" {
					if event, ok := payload.(map[string]any); ok {
						if response, ok := event["response"].(map[string]any); ok {
							response["model"] = publicModel
						}
					}
				}
				b, err := json.Marshal(payload)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", name, b)
				return err
			}
			err := translate.DecodeUpstreamStream(protocol, resp.Body, emit)
			if err != nil {
				_ = emit("error", map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": "upstream stream conversion failed"}})
			}
			_ = writer.CloseWithError(err)
		}()
	} else {
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, (32<<20)+1))
		if err != nil {
			return nil, err
		}
		if len(raw) > 32<<20 {
			return nil, fmt.Errorf("upstream response exceeds 32 MiB")
		}
		body, err := translate.DecodeUpstreamResponse(protocol, raw)
		if err != nil {
			return nil, err
		}
		if publicModel != "" {
			var obj map[string]json.RawMessage
			_ = json.Unmarshal(body, &obj)
			obj["model"], _ = json.Marshal(publicModel)
			body, _ = json.Marshal(obj)
		}
		copied.Body = io.NopCloser(bytes.NewReader(body))
		copied.Header.Set("Content-Type", "application/json")
	}
	return &copied, nil
}

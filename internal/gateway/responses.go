package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"ai-gateway-go/internal/translate"
)

// responses forwards the native protocol without narrowing its fields through
// either the Messages or Chat Completions compatibility representation.
func (h *handler) responses(w http.ResponseWriter, r *http.Request, raw []byte, model string) {
	var root map[string]json.RawMessage
	var request struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if json.Unmarshal(raw, &root) != nil || root == nil || json.Unmarshal(raw, &request) != nil || strings.TrimSpace(request.Model) == "" {
		writeError(w, 400, "invalid_request_error", "a JSON object with model and boolean stream is required")
		return
	}
	if model != "" {
		root["model"], _ = json.Marshal(model)
		raw, _ = json.Marshal(root)
	}
	endpoint := h.endpoint
	if r.URL.RawQuery != "" {
		endpoint += "?" + r.URL.RawQuery
	}
	resp, err := h.do(r, http.MethodPost, endpoint, raw, request.Stream)
	if err != nil {
		if r.Context().Err() == nil {
			writeError(w, 502, "api_error", "upstream connection failed")
		}
		return
	}
	defer resp.Body.Close()
	copyResponseHeaders(w, resp)
	isSSE := strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
	success := resp.StatusCode >= 200 && resp.StatusCode < 300
	if success && request.Stream {
		if !isSSE {
			writeError(w, 502, "api_error", "upstream did not return an SSE stream")
			return
		}
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(resp.StatusCode)
		// Flush each upstream read; preserve event names, payloads, and order.
		_, _ = io.Copy(flushingWriter{w, http.NewResponseController(w)}, resp.Body)
		return
	}
	var body []byte
	if success && isSSE {
		body, err = translate.AggregateStream(resp.Body)
	} else {
		body, err = io.ReadAll(io.LimitReader(resp.Body, (32<<20)+1))
		if len(body) > 32<<20 {
			err = fmt.Errorf("upstream response exceeds 32 MiB")
		}
	}
	if err != nil {
		writeDetailedError(w, err)
		return
	}
	contentType := resp.Header.Get("Content-Type")
	if success && isSSE || contentType == "" {
		contentType = "application/json"
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

type flushingWriter struct {
	w          http.ResponseWriter
	controller *http.ResponseController
}

func (f flushingWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if err == nil {
		err = f.controller.Flush()
	}
	return n, err
}

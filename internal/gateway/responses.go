package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"ai-gateway-go/internal/translate"
)

// responses forwards the native protocol without narrowing its fields through
// either the Messages or Chat Completions compatibility representation.
func (h *handler) responses(w http.ResponseWriter, r *http.Request, raw []byte, model string) {
	h.nativeResponse(w, r, raw, model, h.endpoint)
}

func (h *handler) nativeResponse(w http.ResponseWriter, r *http.Request, raw []byte, model, endpoint string) {
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
	if h.protocol == "openai-compatible" && protocolForPath(r.URL.Path) == h.protocol && h.capabilities.ChatTokenLimitField != "" {
		var err error
		raw, err = translate.EncodeUpstreamRequestWithContext(raw, &translate.ConversionContext{Source: translate.ChatProtocol, Target: translate.ChatProtocol, Capabilities: h.capabilities})
		if err != nil {
			h.conversionError(w, r, 400, err)
			return
		}
	}
	if r.URL.Path == "/v1/messages/count_tokens" {
		delete(root, "stream")
		raw, _ = json.Marshal(root)
		request.Stream = false
	}
	native, _ := r.Context().Value(nativeUpstreamKey{}).(bool)
	if !native && h.protocol != "" && h.protocol != "openai-responses" {
		r = r.WithContext(context.WithValue(r.Context(), responseModelKey{}, request.Model))
	}
	if r.URL.RawQuery != "" {
		endpoint += "?" + r.URL.RawQuery
	}
	resp, err := h.do(r, http.MethodPost, endpoint, raw, request.Stream)
	if err != nil {
		if resp != nil {
			copyResponseHeaders(w, resp)
			resp.Body.Close()
		}
		var invalid *requestEncodingError
		if errors.As(err, &invalid) {
			h.conversionError(w, r, 400, invalid)
			return
		}
		var conversion *translate.ConversionError
		if errors.As(err, &conversion) {
			h.conversionError(w, r, 502, conversion)
			return
		}
		if r.Context().Err() == nil {
			writeError(w, timeoutStatus(err), "api_error", "upstream connection failed")
		}
		return
	}
	defer resp.Body.Close()
	copyResponseHeaders(w, resp)
	isSSE := strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
	success := resp.StatusCode >= 200 && resp.StatusCode < 300
	if !success {
		count := r.URL.Path == "/v1/messages/count_tokens"
		if (!native && h.protocol != "" && h.protocol != "openai-responses") || (count && (resp.StatusCode == 404 || resp.StatusCode == 405 || resp.StatusCode == 501)) {
			h.upstreamError(w, resp, count)
			return
		}
	}
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
		if _, err := io.Copy(flushingWriter{w, http.NewResponseController(w)}, resp.Body); err != nil {
			setOutcome(r, "stream_error")
			// Returning normally would send a successful HTTP transport terminator.
			if native || h.protocol == "" || h.protocol == "openai-responses" {
				panic(http.ErrAbortHandler)
			}
		}
		return
	}
	var body []byte
	if success && isSSE {
		native, _ := r.Context().Value(nativeUpstreamKey{}).(bool)
		if native && h.protocol != "openai-responses" {
			resp, err = normalizeUpstreamResponse(resp, translate.Protocol(h.protocol), "")
			if err != nil {
				writeDetailedError(w, err)
				return
			}
			defer resp.Body.Close()
		}
		body, err = translate.AggregateStream(resp.Body)
		if err == nil && native && h.protocol != "openai-responses" {
			var source struct{ ID, Model string }
			_ = json.Unmarshal(body, &source)
			if h.protocol == "openai-compatible" {
				body, err = translate.ConvertResponseForChat(body, source.Model)
				if err == nil {
					body, err = translate.ChatResponse(body)
				}
			} else {
				body, err = translate.ConvertResponse(body, source.Model)
			}
			if err == nil {
				var result map[string]json.RawMessage
				_ = json.Unmarshal(body, &result)
				result["id"], _ = json.Marshal(source.ID)
				body, err = json.Marshal(result)
			}
		}
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

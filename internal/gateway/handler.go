package gateway

import (
	"ai-gateway-go/internal/translate"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type Config struct {
	BaseURL, APIKey, Model, GatewayKey string
	ProxyURL                           string
	RequestLogDir                      string
	Client                             *http.Client
	ModelAliases                       map[string]string
	ReasoningEfforts                   []string
	ModelReasoningEfforts              map[string][]string
}
type handler struct {
	config   Config
	endpoint string
	client   *http.Client
}

func New(config Config) (http.Handler, error) {
	u, err := url.Parse(config.BaseURL)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("OPENAI_BASE_URL must be an absolute HTTP(S) URL without credentials, query or fragment")
	}
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY is required")
	}
	client := http.Client{}
	if config.Client != nil {
		client = *config.Client
	}
	if config.ProxyURL != "" {
		proxy, parseErr := url.Parse(config.ProxyURL)
		if parseErr != nil || proxy.Hostname() == "" || (proxy.Scheme != "http" && proxy.Scheme != "https" && proxy.Scheme != "socks5" && proxy.Scheme != "socks5h") || (proxy.Path != "" && proxy.Path != "/") || proxy.RawQuery != "" || proxy.Fragment != "" {
			return nil, fmt.Errorf("OPENAI_PROXY_URL must be an HTTP(S) or SOCKS5 proxy URL without path, query or fragment")
		}
		transport := http.DefaultTransport.(*http.Transport)
		if client.Transport != nil {
			var ok bool
			transport, ok = client.Transport.(*http.Transport)
			if !ok {
				return nil, fmt.Errorf("OPENAI_PROXY_URL requires an http.Transport")
			}
		}
		transport = transport.Clone()
		transport.Proxy = http.ProxyURL(proxy)
		client.Transport = transport
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &handler{config: config, endpoint: strings.TrimRight(config.BaseURL, "/") + "/responses", client: &client}, nil
}
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	w.Header().Set("request-id", "req_"+hex.EncodeToString(id))
	r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, w.Header().Get("request-id")))
	chat := r.URL.Path == "/v1/chat/completions"
	native := r.URL.Path == "/v1/responses"
	r = r.WithContext(context.WithValue(r.Context(), requestLogEnabledKey{}, r.URL.Path == "/v1/messages" || chat || native))
	models := r.URL.Path == "/v1/models" || strings.HasPrefix(r.URL.Path, "/v1/models/")
	count := r.URL.Path == "/v1/messages/count_tokens"
	if !models && !count && !chat && !native && r.URL.Path != "/v1/messages" {
		writeError(w, 404, "not_found_error", "unknown endpoint")
		return
	}
	method := http.MethodPost
	if models {
		method = http.MethodGet
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		writeError(w, 405, "invalid_request_error", method+" is required")
		return
	}
	var raw []byte
	var err error
	if !models {
		raw, err = io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
		h.logRequest(r, "inbound", raw)
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeError(w, 413, "request_too_large", "request body exceeds 16 MiB")
			} else {
				writeError(w, 400, "invalid_request_error", "could not read request body")
			}
			return
		}
	}
	if h.config.GatewayKey != "" {
		key := r.Header.Get("x-api-key")
		if key == "" {
			key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		if subtle.ConstantTimeCompare([]byte(key), []byte(h.config.GatewayKey)) != 1 {
			writeError(w, 401, "authentication_error", "invalid gateway API key")
			return
		}
	}
	if models {
		h.models(w, r)
		return
	}
	var request struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	_ = json.Unmarshal(raw, &request)
	if count {
		var v map[string]any
		if json.Unmarshal(raw, &v) == nil && v != nil {
			if _, ok := v["max_tokens"]; !ok {
				v["max_tokens"] = 2147483647
			}
			v["stream"] = false
			raw, _ = json.Marshal(v)
		}
	}
	model := h.config.Model
	if mapped, ok := h.config.ModelAliases[request.Model]; ok {
		model = mapped
	}
	if native {
		h.responses(w, r, raw, model)
		return
	}
	efforts := h.config.ReasoningEfforts
	effective := model
	if effective == "" {
		effective = request.Model
	}
	if configured, ok := h.config.ModelReasoningEfforts[effective]; ok {
		efforts = configured
	}
	var prepared translate.PreparedRequest
	includeUsage := false
	options := translate.RequestOptions{Model: model, ReasoningEfforts: efforts}
	if chat {
		prepared, includeUsage, err = translate.PrepareChatRequest(raw, options)
	} else {
		prepared, err = translate.PrepareRequest(raw, options)
	}
	if err != nil {
		writeError(w, 400, "invalid_request_error", err.Error())
		return
	}
	if len(prepared.Warnings) > 0 {
		warnings := make([]string, len(prepared.Warnings))
		for i, s := range prepared.Warnings {
			warnings[i] = url.QueryEscape(s)
		}
		w.Header().Set("X-Gateway-Warnings", strings.Join(warnings, ","))
	}
	endpoint := h.endpoint
	body := prepared.Body
	if count {
		endpoint += "/input_tokens"
		var v map[string]any
		_ = json.Unmarshal(body, &v)
		for _, key := range []string{"max_output_tokens", "stream", "store", "temperature", "top_p", "text", "tool_choice", "parallel_tool_calls", "include", "metadata", "service_tier", "background"} {
			delete(v, key)
		}
		body, _ = json.Marshal(v)
	}
	resp, err := h.do(r, http.MethodPost, endpoint, body, request.Stream && !count)
	if err != nil {
		if r.Context().Err() == nil {
			writeError(w, 502, "api_error", "upstream connection failed")
		}
		return
	}
	defer resp.Body.Close()
	copyResponseHeaders(w, resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		h.upstreamError(w, resp, count)
		return
	}
	if count {
		h.tokenCount(w, resp)
		return
	}
	if request.Stream {
		if !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
			writeError(w, 502, "api_error", "upstream did not return an SSE stream")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		controller := http.NewResponseController(w)
		emit := func(name string, payload any) error {
			b, e := json.Marshal(payload)
			if e != nil {
				return e
			}
			if _, e = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b); e != nil {
				return e
			}
			return controller.Flush()
		}
		if chat {
			bridge := translate.NewChatStream(request.Model, includeUsage, func(chunk any) error {
				if chunk == nil {
					if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
						return err
					}
				} else {
					b, err := json.Marshal(chunk)
					if err != nil {
						return err
					}
					if _, err = fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
						return err
					}
				}
				return controller.Flush()
			})
			emit = bridge.Emit
		}
		filter := newOutputFilter(prepared, emit)
		convertStream := translate.ConvertStream
		if chat {
			convertStream = translate.ConvertStreamForChat
		}
		if err = convertStream(resp.Body, request.Model, filter.Emit); err != nil && r.Context().Err() == nil {
			_ = emit("error", detailedError(w, err))
		}
		return
	}
	var responseBody []byte
	if strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		responseBody, err = translate.AggregateStream(resp.Body)
	} else {
		responseBody, err = io.ReadAll(io.LimitReader(resp.Body, (32<<20)+1))
		if len(responseBody) > 32<<20 {
			err = fmt.Errorf("upstream response exceeds 32 MiB")
		}
	}
	if err != nil {
		writeDetailedError(w, err)
		return
	}
	convertResponse := translate.ConvertResponse
	if chat {
		convertResponse = translate.ConvertResponseForChat
	}
	converted, err := convertResponse(responseBody, request.Model)
	if err == nil {
		converted, err = transformResponse(converted, prepared)
	}
	if err == nil && chat {
		converted, err = translate.ChatResponse(converted)
	}
	if err != nil {
		writeDetailedError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(converted)
}
func (h *handler) do(r *http.Request, method, endpoint string, body []byte, stream bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(r.Context(), method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+h.config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	if req.URL.Hostname() == "opencode.ai" {
		req.Header.Set("User-Agent", "ai-gateway-go/1.0")
		req.Header.Set("x-opencode-session", openCodeSession(r, body))
	}
	req.Header.Set("Accept", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	h.logRequest(req, "upstream", body)
	req = h.traceRequestHeaders(req, body)
	return h.client.Do(req)
}
func copyResponseHeaders(w http.ResponseWriter, resp *http.Response) {
	id := resp.Header.Get("x-request-id")
	if id == "" {
		id = resp.Header.Get("request-id")
	}
	if id != "" {
		w.Header().Set("request-id", id)
	}
	if retry := resp.Header.Get("Retry-After"); retry != "" {
		w.Header().Set("Retry-After", retry)
	}
}
func errorType(status int) string {
	switch status {
	case 400, 405, 422:
		return "invalid_request_error"
	case 401:
		return "authentication_error"
	case 403:
		return "permission_error"
	case 404:
		return "not_found_error"
	case 413:
		return "request_too_large"
	case 429:
		return "rate_limit_error"
	case 529:
		return "overloaded_error"
	default:
		return "api_error"
	}
}
func errorBody(kind, message string) map[string]any {
	return map[string]any{"type": "error", "error": map[string]any{"type": kind, "message": message}}
}
func writeError(w http.ResponseWriter, status int, kind, message string) {
	v := errorBody(kind, message)
	v["request_id"] = w.Header().Get("request-id")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func detailedError(w http.ResponseWriter, err error) map[string]any {
	v := errorBody("api_error", err.Error())
	v["request_id"] = w.Header().Get("request-id")
	var upstream *translate.UpstreamError
	if errors.As(err, &upstream) {
		detail := v["error"].(map[string]any)
		detail["type"] = structuredErrorType(upstream.Type, upstream.Code)
		if upstream.Code != "" {
			detail["code"] = upstream.Code
		}
		if upstream.Param != "" {
			detail["param"] = upstream.Param
		}
		if upstream.RequestID != "" {
			v["request_id"] = upstream.RequestID
			w.Header().Set("request-id", upstream.RequestID)
		}
	}
	return v
}
func structuredErrorType(kind, code string) string {
	for _, s := range []string{kind, code} {
		switch s {
		case "rate_limit_error", "rate_limit_exceeded", "insufficient_quota":
			return "rate_limit_error"
		case "invalid_request_error", "authentication_error", "permission_error", "not_found_error", "overloaded_error", "api_error":
			return s
		case "server_error":
			return "api_error"
		}
	}
	return "api_error"
}
func writeDetailedError(w http.ResponseWriter, err error) {
	v := detailedError(w, err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(502)
	_ = json.NewEncoder(w).Encode(v)
}
func (h *handler) upstreamError(w http.ResponseWriter, resp *http.Response, count bool) {
	status := resp.StatusCode
	if count && (status == 404 || status == 405 || status == 501) {
		writeError(w, 501, "not_supported_error", "upstream does not support exact input token counting")
		return
	}
	if status < 400 || status > 599 {
		status = 502
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var v struct {
		Error struct{ Message, Type, Code, Param string }
	}
	_ = json.Unmarshal(raw, &v)
	message := v.Error.Message
	if message == "" {
		message = fmt.Sprintf("upstream returned HTTP %d", resp.StatusCode)
	}
	body := errorBody(errorType(status), message)
	body["request_id"] = w.Header().Get("request-id")
	detail := body["error"].(map[string]any)
	if v.Error.Code != "" {
		detail["code"] = v.Error.Code
	}
	if v.Error.Param != "" {
		detail["param"] = v.Error.Param
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
func (h *handler) tokenCount(w http.ResponseWriter, resp *http.Response) {
	var v struct {
		InputTokens any `json:"input_tokens"`
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	decoder.UseNumber()
	if err := decoder.Decode(&v); err != nil || v.InputTokens == nil {
		writeError(w, 502, "api_error", "upstream returned invalid input_tokens")
		return
	}
	number, ok := v.InputTokens.(json.Number)
	if !ok {
		writeError(w, 502, "api_error", "upstream returned invalid input_tokens")
		return
	}
	n, err := number.Int64()
	if err != nil || n < 0 {
		writeError(w, 502, "api_error", "upstream returned invalid input_tokens")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"input_tokens": n})
}

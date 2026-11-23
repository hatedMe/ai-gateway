package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type modelInfo struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
}

func (h *handler) models(w http.ResponseWriter, r *http.Request) {
	var models []modelInfo
	if len(h.config.ModelAliases) > 0 {
		for alias := range h.config.ModelAliases {
			models = append(models, modelInfo{alias, "model", alias, "1970-01-01T00:00:00Z"})
		}
	} else {
		resp, err := h.do(r, http.MethodGet, strings.TrimSuffix(h.endpoint, "/responses")+"/models", nil, false)
		if err != nil {
			writeError(w, 502, "api_error", "upstream connection failed")
			return
		}
		defer resp.Body.Close()
		copyResponseHeaders(w, resp)
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			h.upstreamError(w, resp, false)
			return
		}
		var payload struct {
			Data []struct {
				ID      string `json:"id"`
				Created int64  `json:"created"`
			}
		}
		if err = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&payload); err != nil {
			writeError(w, 502, "api_error", "upstream returned invalid models")
			return
		}
		for _, m := range payload.Data {
			models = append(models, modelInfo{m.ID, "model", m.ID, time.Unix(m.Created, 0).UTC().Format(time.RFC3339)})
		}
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path != "/v1/models" {
		id := strings.TrimPrefix(r.URL.Path, "/v1/models/")
		for _, m := range models {
			if m.ID == id {
				_ = json.NewEncoder(w).Encode(m)
				return
			}
		}
		writeError(w, 404, "not_found_error", "model not found")
		return
	}
	limit := 20
	if value := r.URL.Query().Get("limit"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 1000 {
			writeError(w, 400, "invalid_request_error", "limit must be between 1 and 1000")
			return
		}
		limit = n
	}
	before, after := r.URL.Query().Get("before_id"), r.URL.Query().Get("after_id")
	if before != "" && after != "" {
		writeError(w, 400, "invalid_request_error", "before_id and after_id cannot be combined")
		return
	}
	if before != "" || after != "" {
		cursor := after
		if before != "" {
			cursor = before
		}
		index := -1
		for i, m := range models {
			if m.ID == cursor {
				index = i
				break
			}
		}
		if index < 0 {
			writeError(w, 400, "invalid_request_error", "unknown model pagination cursor")
			return
		}
		if before != "" {
			models = models[:index]
		} else {
			models = models[index+1:]
		}
	}
	more := len(models) > limit
	if more {
		models = models[:limit]
	}
	if models == nil {
		models = []modelInfo{}
	}
	var first, last any
	if len(models) > 0 {
		first = models[0].ID
		last = models[len(models)-1].ID
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": models, "has_more": more, "first_id": first, "last_id": last})
}

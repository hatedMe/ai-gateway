package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type requestIDKey struct{}
type requestLogEnabledKey struct{}

func sensitiveField(name string) bool {
	name = strings.ToLower(name)
	return strings.Contains(name, "authorization") || strings.Contains(name, "api-key") || strings.Contains(name, "api_key") || strings.Contains(name, "token") || strings.Contains(name, "secret") || strings.Contains(name, "password") || strings.Contains(name, "cookie")
}

func (h *handler) logRequest(r *http.Request, stage string, body []byte) {
	source := "inbound"
	if stage == "upstream" {
		source = "prepared"
	}
	h.logRequestHeaders(r, stage, body, r.Header, source)
}

func (h *handler) logRequestHeaders(r *http.Request, stage string, body []byte, actualHeaders http.Header, source string) {
	enabled, _ := r.Context().Value(requestLogEnabledKey{}).(bool)
	if h.config.RequestLogDir == "" || !enabled {
		return
	}
	id, _ := r.Context().Value(requestIDKey{}).(string)
	headers := actualHeaders.Clone()
	for name := range headers {
		if sensitiveField(name) {
			headers[name] = []string{"[REDACTED]"}
		}
	}
	// Host and the transport's default User-Agent are not stored in Header.
	if headers.Get("Host") == "" {
		host := r.Host
		if host == "" {
			host = r.URL.Host
		}
		headers.Set("Host", host)
	}
	query := r.URL.Query()
	for name := range query {
		if sensitiveField(name) {
			query[name] = []string{"[REDACTED]"}
		}
	}
	var content any
	if len(body) > 0 {
		if json.Valid(body) {
			content = json.RawMessage(body)
		} else {
			content = string(body)
		}
	}
	record := struct {
		Time         string      `json:"time"`
		RequestID    string      `json:"request_id"`
		Stage        string      `json:"stage"`
		Method       string      `json:"method"`
		URL          string      `json:"url"`
		Query        url.Values  `json:"query"`
		Headers      http.Header `json:"headers"`
		Body         any         `json:"body"`
		RawBody      string      `json:"raw_body"`
		BodyBytes    int         `json:"body_bytes"`
		HeaderSource string      `json:"header_source"`
	}{time.Now().Format(time.RFC3339Nano), id, stage, r.Method, r.URL.Scheme + "://" + r.URL.Host + r.URL.EscapedPath(), query, headers, content, string(body), len(body), source}
	if r.URL.Scheme == "" {
		record.URL = r.URL.EscapedPath()
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err == nil {
		err = os.MkdirAll(h.config.RequestLogDir, 0700)
	}
	if err == nil {
		err = os.WriteFile(filepath.Join(h.config.RequestLogDir, id+"_"+stage+".json"), append(raw, '\n'), 0600)
	}
	if err == nil {
		uri := r.URL.EscapedPath()
		if uri == "" {
			uri = "/"
		}
		if len(query) > 0 {
			uri += "?" + query.Encode()
		}
		proto := r.Proto
		if proto == "" {
			proto = "HTTP/1.1"
		}
		if headers.Get(":method") != "" {
			proto = "HTTP/2.0"
		}
		var request bytes.Buffer
		fmt.Fprintf(&request, "%s %s %s\r\n", r.Method, uri, proto)
		names := make([]string, 0, len(headers))
		for name := range headers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			for _, value := range headers[name] {
				fmt.Fprintf(&request, "%s: %s\r\n", name, value)
			}
		}
		request.WriteString("\r\n")
		request.Write(body)
		err = os.WriteFile(filepath.Join(h.config.RequestLogDir, id+"_"+stage+".http"), request.Bytes(), 0600)
	}
	if err != nil {
		log.Printf("request log write failed request_id=%s stage=%s: %v", id, stage, err)
	}
}

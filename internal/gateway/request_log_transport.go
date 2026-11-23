package gateway

import (
	"net/http"
	"net/http/httptrace"
	"sync"
)

// Capture headers after the transport has added Content-Length, compression,
// HTTP/2 pseudo headers, etc. The prepared record remains if no bytes are sent.
func (h *handler) traceRequestHeaders(r *http.Request, body []byte) (*http.Request, func()) {
	enabled, _ := r.Context().Value(requestLogEnabledKey{}).(bool)
	if h.config.RequestLogDir == "" || !enabled {
		return r, func() {}
	}
	var mu sync.Mutex
	headers := make(http.Header)
	var snapshot http.Header
	trace := &httptrace.ClientTrace{
		WroteHeaderField: func(key string, values []string) {
			mu.Lock()
			headers[http.CanonicalHeaderKey(key)] = append([]string(nil), values...)
			mu.Unlock()
		},
		WroteHeaders: func() {
			mu.Lock()
			snapshot = headers.Clone()
			headers = make(http.Header)
			mu.Unlock()
		},
	}
	return r.WithContext(httptrace.WithClientTrace(r.Context(), trace)), func() {
		mu.Lock()
		actual := snapshot.Clone()
		mu.Unlock()
		source := "transport"
		if actual == nil {
			actual = r.Header
			source = "prepared"
		}
		h.logRequestHeaders(r, "upstream", body, actual, source)
	}
}

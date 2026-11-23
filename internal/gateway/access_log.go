package gateway

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"
)

type accessKey struct{}
type accessRecord struct {
	mu          sync.Mutex
	started     time.Time
	ID          string `json:"request_id"`
	UpstreamID  string `json:"upstream_request_id,omitempty"`
	Upstream    string `json:"upstream,omitempty"`
	Protocol    string `json:"upstream_protocol,omitempty"`
	Status      int    `json:"status"`
	Bytes       int    `json:"bytes"`
	DurationMS  int64  `json:"duration_ms"`
	FirstByteMS int64  `json:"upstream_first_byte_ms"`
	Outcome     string `json:"outcome"`
}

func access(r *http.Request) *accessRecord {
	a, _ := r.Context().Value(accessKey{}).(*accessRecord)
	return a
}
func responseCommitted(r *http.Request) bool {
	if a := access(r); a != nil {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.Status != 0
	}
	return false
}

func setOutcome(r *http.Request, reason string) {
	if a := access(r); a != nil {
		a.mu.Lock()
		a.Outcome = reason
		a.mu.Unlock()
	}
}

type accessWriter struct {
	http.ResponseWriter
	record *accessRecord
}

func (w *accessWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *accessWriter) WriteHeader(status int) {
	w.record.mu.Lock()
	first := w.record.Status == 0
	if first {
		w.record.Status = status
		if status >= 400 && w.record.Outcome == "success" {
			w.record.Outcome = "http_error"
		}
		if status == 504 {
			w.record.Outcome = "upstream_timeout"
		}
	}
	w.record.mu.Unlock()
	if first {
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *accessWriter) Write(p []byte) (int, error) {
	w.WriteHeader(200)
	n, err := w.ResponseWriter.Write(p)
	w.record.mu.Lock()
	w.record.Bytes += n
	if err != nil {
		w.record.Outcome = "downstream_error"
	}
	w.record.mu.Unlock()
	return n, err
}
func (w *accessWriter) FlushError() error {
	w.WriteHeader(200)
	return http.NewResponseController(w.ResponseWriter).Flush()
}
func (a *accessRecord) finish(r *http.Request, w http.ResponseWriter) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Status == 0 {
		a.Status = 200
	}
	a.UpstreamID = w.Header().Get("x-upstream-request-id")
	a.DurationMS = time.Since(a.started).Milliseconds()
	if r.Context().Err() != nil {
		a.Outcome = "client_canceled"
	}
	raw, _ := json.Marshal(a)
	log.Printf("access %s", raw)
}

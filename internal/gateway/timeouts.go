package gateway

import (
	"ai-gateway-go/internal/translate"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

func timeoutStatus(err error) int {
	var upstream *translate.UpstreamError
	if errors.As(err, &upstream) && upstream.Code == "upstream_timeout" {
		return 504
	}
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return 504
	}
	return 502
}

// Stop waits for an already running callback so it cannot cancel a later phase.
func phaseTimer(timeout time.Duration, cancel context.CancelFunc) func() bool {
	done := make(chan struct{})
	timer := time.AfterFunc(timeout, func() { cancel(); close(done) })
	return func() bool {
		if timer.Stop() {
			return false
		}
		<-done
		return true
	}
}

type idleBody struct {
	io.ReadCloser
	cancel  context.CancelFunc
	timeout time.Duration
}

func (b *idleBody) Read(p []byte) (int, error) {
	stop := phaseTimer(b.timeout, b.cancel)
	n, err := b.ReadCloser.Read(p)
	if stop() {
		return n, os.ErrDeadlineExceeded
	}
	return n, err
}
func (b *idleBody) Close() error { b.cancel(); return b.ReadCloser.Close() }

type deadlineWriter struct {
	http.ResponseWriter
	timeout time.Duration
}

func (w deadlineWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w deadlineWriter) renew() {
	_ = http.NewResponseController(w.ResponseWriter).SetWriteDeadline(time.Now().Add(w.timeout))
}
func (w deadlineWriter) WriteHeader(status int)      { w.renew(); w.ResponseWriter.WriteHeader(status) }
func (w deadlineWriter) Write(p []byte) (int, error) { w.renew(); return w.ResponseWriter.Write(p) }
func (w deadlineWriter) FlushError() error {
	w.renew()
	return http.NewResponseController(w.ResponseWriter).Flush()
}

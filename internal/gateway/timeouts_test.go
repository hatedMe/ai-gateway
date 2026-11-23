package gateway

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGatewayTimeouts(t *testing.T) {
	for _, phase := range []string{"headers", "body", "active"} {
		t.Run(phase, func(t *testing.T) {
			canceled := make(chan struct{})
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(canceled)
				_, _ = io.Copy(io.Discard, r.Body)
				if phase == "headers" {
					<-r.Context().Done()
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				if phase == "body" {
					<-r.Context().Done()
					return
				}
				for i := 0; i < 8; i++ {
					fmt.Fprint(w, " ")
					w.(http.Flusher).Flush()
					time.Sleep(15 * time.Millisecond)
				}
				fmt.Fprint(w, `{"ok":true}`)
			}))
			defer up.Close()
			h, err := New(Config{BaseURL: up.URL, APIKey: "test", ResponseHeaderTimeout: 80 * time.Millisecond, ReadIdleTimeout: 80 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m","input":"hi"}`)))
			want := 504
			if phase == "active" {
				want = 200
			}
			if w.Code != want {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			select {
			case <-canceled:
			case <-time.After(2 * time.Second):
				t.Fatal("upstream did not finish")
			}
		})
	}
	t.Run("request-body", func(t *testing.T) {
		h, _ := New(Config{BaseURL: "http://unused.invalid", APIKey: "test", ReadBodyTimeout: 40 * time.Millisecond})
		s := httptest.NewServer(h)
		defer s.Close()
		c, err := net.Dial("tcp", strings.TrimPrefix(s.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		fmt.Fprint(c, "POST /v1/responses HTTP/1.1\r\nHost: localhost\r\nContent-Length: 100\r\n\r\n")
		r, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		if r.StatusCode != 408 {
			b, _ := io.ReadAll(r.Body)
			t.Fatalf("%d %s", r.StatusCode, b)
		}
	})
}

func TestGatewaySlowDownstreamReleasesUpstream(t *testing.T) {
	done := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := strings.Repeat("x", 64<<10)
		for {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			if r.Context().Err() != nil {
				return
			}
		}
	}))
	defer up.Close()
	h, _ := New(Config{BaseURL: up.URL, APIKey: "test", WriteIdleTimeout: 40 * time.Millisecond})
	s := httptest.NewServer(h)
	defer s.Close()
	c, err := net.Dial("tcp", strings.TrimPrefix(s.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	body := `{"model":"m","input":"hi","stream":true}`
	fmt.Fprintf(c, "POST /v1/responses HTTP/1.1\r\nHost: localhost\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("slow downstream held upstream")
	}
}

func TestReadIdleExcludesConsumerDelay(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ab") }))
	defer up.Close()
	h, _ := New(Config{BaseURL: up.URL, APIKey: "test", ReadIdleTimeout: 20 * time.Millisecond})
	r, _ := http.NewRequest("GET", up.URL, nil)
	resp, err := h.(*handler).do(r, "GET", up.URL, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b := make([]byte, 1)
	if _, err = resp.Body.Read(b); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err = resp.Body.Read(b); (err != nil && err != io.EOF) || b[0] != 'b' {
		t.Fatalf("consumer delay counted as upstream idle: %q %v", b, err)
	}
}

package gateway

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type forbiddenBody struct{ reads int }

func (b *forbiddenBody) Read([]byte) (int, error) {
	b.reads++
	return 0, errors.New("body must not be read")
}

func TestUnauthorizedRequestDoesNotReadBody(t *testing.T) {
	dir := t.TempDir()
	h, err := New(Config{BaseURL: "http://unused.invalid/v1", APIKey: "test", GatewayKey: "expected", RequestLogDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	body := &forbiddenBody{}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", body))
	files, _ := os.ReadDir(dir)
	if w.Code != 401 || body.reads != 0 || len(files) != 0 {
		t.Fatalf("status=%d reads=%d files=%d", w.Code, body.reads, len(files))
	}
}

func TestUnauthorizedRequestWithUnsentBody(t *testing.T) {
	h, _ := New(Config{BaseURL: "http://unused.invalid/v1", APIKey: "test", GatewayKey: "expected"})
	s := httptest.NewServer(h)
	defer s.Close()
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(s.URL, "http://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	_, _ = fmt.Fprint(conn, "POST /v1/messages HTTP/1.1\r\nHost: localhost\r\nContent-Length: 100\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal(resp.StatusCode)
	}
}

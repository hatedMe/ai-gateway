package gateway

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-go/internal/translate"
)

func BenchmarkOutputFilter(b *testing.B) {
	for _, stop := range []bool{false, true} {
		name := "plain"
		if stop {
			name = "stop-prefix"
		}
		b.Run(name, func(b *testing.B) {
			prepared := translate.PreparedRequest{}
			if stop {
				prepared.StopSequences = []string{"END"}
			}
			filter := newOutputFilter(prepared, func(string, any) error { return nil })
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if err := filter.Emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "sample text"}}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkGatewayRequestLogging(b *testing.B) {
	old := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(old)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"ok":true}`) }))
	defer up.Close()
	for _, enabled := range []bool{false, true} {
		b.Run(fmt.Sprint(enabled), func(b *testing.B) {
			dir := ""
			if enabled {
				dir = b.TempDir()
			}
			h, err := New(Config{BaseURL: up.URL, APIKey: "test", RequestLogDir: dir})
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m","input":"hi"}`)))
				if w.Code != 200 {
					b.Fatal(w.Code)
				}
			}
		})
	}
}

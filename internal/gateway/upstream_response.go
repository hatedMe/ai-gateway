package gateway

import (
	"ai-gateway-go/internal/translate"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type requestEncodingError struct{ error }

func (e *requestEncodingError) Unwrap() error { return e.error }

func normalizeUpstreamResponse(resp *http.Response, protocol translate.Protocol, publicModel string) (*http.Response, error) {
	return normalizeUpstreamResponseWithContext(resp, &translate.ConversionContext{Target: protocol, ClientModel: publicModel})
}
func normalizeUpstreamResponseWithContext(resp *http.Response, c *translate.ConversionContext) (*http.Response, error) {
	return normalizeUpstreamResponseObserved(resp, c, nil)
}
func normalizeUpstreamResponseObserved(resp *http.Response, c *translate.ConversionContext, onError func(error)) (*http.Response, error) {
	copied := *resp
	copied.Header = resp.Header.Clone()
	copied.Header.Del("Content-Length")
	copied.ContentLength = -1
	if strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		reader, writer := io.Pipe()
		copied.Body = &conversionBody{ReadCloser: reader, source: resp.Body}
		go func() {
			defer resp.Body.Close()
			emit := func(name string, payload any) error {
				b, err := json.Marshal(payload)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", name, b)
				return err
			}
			err := translate.DecodeUpstreamStreamWithContext(resp.Body, c, emit)
			if err != nil {
				if onError != nil {
					onError(err)
				}
				detail := map[string]any{"type": "api_error", "message": "upstream stream conversion failed"}
				var conversion *translate.ConversionError
				if errors.As(err, &conversion) {
					detail["code"], detail["stage"], detail["field"] = conversion.Code, conversion.Stage, conversion.Field
				}
				_ = emit("error", map[string]any{"type": "error", "error": detail})
			}
			_ = writer.CloseWithError(err)
		}()
	} else {
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, (32<<20)+1))
		if err != nil {
			return nil, err
		}
		if len(raw) > 32<<20 {
			return nil, fmt.Errorf("upstream response exceeds 32 MiB")
		}
		body, err := translate.DecodeUpstreamResponseWithContext(raw, c)
		if err != nil {
			return nil, err
		}
		copied.Body = io.NopCloser(bytes.NewReader(body))
		copied.Header.Set("Content-Type", "application/json")
	}
	return &copied, nil
}

type conversionBody struct {
	io.ReadCloser
	source io.Closer
}

func (b *conversionBody) Close() error { err := b.ReadCloser.Close(); b.source.Close(); return err }

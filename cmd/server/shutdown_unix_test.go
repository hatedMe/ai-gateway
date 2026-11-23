//go:build !windows

package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestShutdownHelperProcess(t *testing.T) {
	if os.Getenv("GATEWAY_SHUTDOWN_HELPER") != "1" {
		return
	}
	if err := run([]string{"--config", os.Getenv("GATEWAY_SHUTDOWN_CONFIG")}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestSIGTERMGracefulShutdown(t *testing.T) {
	for _, finish := range []bool{true, false} {
		t.Run(fmt.Sprint(finish), func(t *testing.T) {
			setupEnv(t)
			started := make(chan struct{})
			release := make(chan struct{})
			canceled := make(chan struct{})
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				close(started)
				select {
				case <-release:
					fmt.Fprint(w, `{"done":true}`)
				case <-r.Context().Done():
					close(canceled)
				}
			}))
			defer up.Close()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := listener.Addr().String()
			listener.Close()
			path := filepath.Join(t.TempDir(), "config.yaml")
			cfg := fmt.Sprintf("server:\n  addr: %q\n  shutdown_timeout: 400ms\nupstream:\n  base_url: %q\n  api_key: test\n  proxy_url: ''\nauth:\n  gateway_api_key: ''\nmodels:\n  aliases: {m: real}\nlogging:\n  requests:\n    enabled: false\n", addr, up.URL)
			if err := os.WriteFile(path, []byte(cfg), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestShutdownHelperProcess$")
			cmd.Env = append(os.Environ(), "GATEWAY_SHUTDOWN_HELPER=1", "GATEWAY_SHUTDOWN_CONFIG="+path)
			var output bytes.Buffer
			cmd.Stdout = &output
			cmd.Stderr = &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			defer func() { _ = cmd.Process.Kill() }()
			client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
			defer client.CloseIdleConnections()
			ready := false
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				r, err := client.Get("http://" + addr + "/v1/models")
				if err == nil {
					r.Body.Close()
					ready = true
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !ready {
				t.Fatal("server not ready")
			}
			result := make(chan error, 1)
			go func() {
				r, err := client.Post("http://"+addr+"/v1/responses", "application/json", strings.NewReader(`{"model":"m","input":"hi"}`))
				if err == nil {
					_, err = io.ReadAll(r.Body)
					r.Body.Close()
				}
				result <- err
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("upstream not called")
			}
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			// Shutdown closes the listener before waiting for active requests.
			closed := false
			deadline = time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				r, err := client.Get("http://" + addr + "/v1/models")
				if err != nil {
					closed = true
					break
				}
				r.Body.Close()
				time.Sleep(10 * time.Millisecond)
			}
			if !closed {
				t.Fatal("listener remained open")
			}
			if finish {
				close(release)
			}
			select {
			case err := <-exited:
				if err != nil {
					t.Fatalf("shutdown: %v %s", err, output.String())
				}
			case <-time.After(3 * time.Second):
				t.Fatal("shutdown hung")
			}
			select {
			case err := <-result:
				if finish && err != nil {
					t.Fatal("active request interrupted", err)
				}
			case <-time.After(time.Second):
				t.Fatal("request not released")
			}
			if !finish {
				select {
				case <-canceled:
				case <-time.After(time.Second):
					t.Fatal("upstream not canceled")
				}
			}
		})
	}
}

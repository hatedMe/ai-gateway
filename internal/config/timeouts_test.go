package config

import (
	"testing"
	"time"
)

func TestTimeoutConfiguration(t *testing.T) {
	cleanEnv(t)
	c, err := Load(writeConfig(t, "server:\n  read_body_timeout: 2s\n  write_idle_timeout: 3s\n  idle_timeout: 4s\nupstream:\n  response_header_timeout: 5s\n  read_idle_timeout: 6s\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.ReadBodyTimeout != 2*time.Second || c.Server.WriteIdleTimeout != 3*time.Second || c.Server.IdleTimeout != 4*time.Second || c.Upstream.ResponseHeaderTimeout != 5*time.Second || c.Upstream.ReadIdleTimeout != 6*time.Second {
		t.Fatal("timeouts not loaded")
	}
	for _, value := range []string{"0s", "-1s", "invalid"} {
		if _, err := Load(writeConfig(t, "server:\n  read_body_timeout: "+value+"\n"), true); err == nil {
			t.Fatal("accepted", value)
		}
	}
}

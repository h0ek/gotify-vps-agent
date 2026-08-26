package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/h0ek/gotify-vps-agent/internal/config"
)

func TestVersionReportsBuildAndGoToolchain(t *testing.T) {
	var output bytes.Buffer
	var errors bytes.Buffer
	code := New(strings.NewReader(""), &output, &errors).Run(context.Background(), []string{"version"})
	if code != 0 {
		t.Fatalf("version returned %d: %s", code, errors.String())
	}
	for _, expected := range []string{"gotify-vps-agent ", "commit=", "built=", "go=go"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("version output %q does not contain %q", output.String(), expected)
		}
	}
}

func TestUnknownCommandFails(t *testing.T) {
	var output bytes.Buffer
	var errors bytes.Buffer
	code := New(strings.NewReader(""), &output, &errors).Run(context.Background(), []string{"unknown"})
	if code != 2 || !strings.Contains(errors.String(), "Unknown command") {
		t.Fatalf("unexpected result code=%d stderr=%q", code, errors.String())
	}
}

func TestProxyStatus(t *testing.T) {
	cfg := config.Default()
	cfg.Gotify.URL = "https://gotify.example"
	cfg.Gotify.ProxyURL = "socks5h://127.0.0.1:9050"
	data, err := config.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var errors bytes.Buffer
	code := New(strings.NewReader(""), &output, &errors).Run(context.Background(), []string{"--config", path, "proxy", "status"})
	if code != 0 {
		t.Fatalf("proxy status returned %d: %s", code, errors.String())
	}
	for _, expected := range []string{"Gotify server: https://gotify.example", "SOCKS5 proxy: enabled via socks5h://127.0.0.1:9050"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("proxy status output %q does not contain %q", output.String(), expected)
		}
	}
}

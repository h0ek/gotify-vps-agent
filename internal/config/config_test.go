package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestMarshalRoundTrip(t *testing.T) {
	original := Default()
	original.Gotify.URL = "https://gotify.example"
	original.Gotify.ProxyURL = "socks5h://127.0.0.1:9050"
	original.Services = map[string]string{
		"nginx":   "nginx.service",
		"php-fpm": "php8.4-fpm.service",
	}
	data, err := Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	decoded := Default()
	decoded.Services = map[string]string{}
	if err := Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, decoded) {
		t.Fatalf("round trip mismatch\noriginal: %#v\ndecoded: %#v", original, decoded)
	}
}

func TestRejectUnknownKey(t *testing.T) {
	cfg := Default()
	err := Unmarshal([]byte("version = 1\n\n[agent]\nunknown = true\n"), &cfg)
	if err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("expected unknown key error, got %v", err)
	}
}

func TestRejectUnsafeServiceUnit(t *testing.T) {
	cfg := Default()
	cfg.Gotify.URL = "https://gotify.example"
	cfg.Services["nginx"] = "--system.service"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected unsafe unit to be rejected")
	}
}

func TestValidateRejectsUnsafeHostname(t *testing.T) {
	cfg := Default()
	cfg.Gotify.URL = "https://gotify.example"
	cfg.Agent.Hostname = "host\nInjected"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected unsafe hostname validation error")
	}
}

func TestValidateRejectsCustomStatePath(t *testing.T) {
	cfg := Default()
	cfg.Gotify.URL = "https://gotify.example"
	cfg.Agent.StateDir = "/tmp/gotify-vps-agent"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected fixed state path validation error")
	}
}

func TestRejectDuplicateKey(t *testing.T) {
	cfg := Default()
	err := Unmarshal([]byte("version = 1\nversion = 1\n"), &cfg)
	if err == nil || !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("expected duplicate key error, got %v", err)
	}
}

func TestValidateGotifyTimeoutBounds(t *testing.T) {
	for _, value := range []string{"999ms", "61s", "invalid"} {
		cfg := Default()
		cfg.Gotify.URL = "https://gotify.example"
		cfg.Gotify.Timeout = value
		if err := cfg.Validate(); err == nil {
			t.Fatalf("expected timeout %q to be rejected", value)
		}
	}
}

func TestValidateRejectsNonFiniteThresholds(t *testing.T) {
	for _, value := range []string{"NaN", "+Inf", "-Inf"} {
		cfg := Default()
		err := Unmarshal([]byte("[thresholds]\ndisk_warning = "+value+"\n"), &cfg)
		if err != nil {
			continue
		}
		cfg.Gotify.URL = "https://gotify.example"
		if err := cfg.Validate(); err == nil {
			t.Fatalf("expected threshold %q to be rejected", value)
		}
	}
}

func TestValidateRejectsUnsafeProxyURL(t *testing.T) {
	for _, value := range []string{
		"http://127.0.0.1:9050",
		"socks5h://192.0.2.10:9050",
		"socks5h://user:pass@127.0.0.1:9050",
		"socks5h://127.0.0.1",
	} {
		cfg := Default()
		cfg.Gotify.URL = "https://gotify.example"
		cfg.Gotify.ProxyURL = value
		if err := cfg.Validate(); err == nil {
			t.Fatalf("expected proxy URL %q to be rejected", value)
		}
	}
}

func TestUnmarshalLegacyConfigWithoutProxy(t *testing.T) {
	cfg := Default()
	data := []byte("version = 1\n[gotify]\nurl = \"https://gotify.example\"\n")
	if err := Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Gotify.ProxyURL != "" {
		t.Fatalf("unexpected proxy URL: %q", cfg.Gotify.ProxyURL)
	}
}

func FuzzUnmarshal(f *testing.F) {
	f.Add([]byte("version = 1\n[gotify]\nurl = \"https://gotify.example\"\n"))
	f.Add([]byte("[services]\nnginx = \"nginx.service\"\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > maxConfigBytes {
			t.Skip()
		}
		cfg := Default()
		_ = Unmarshal(data, &cfg)
	})
}

package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/h0ek/gotify-vps-agent/internal/config"
	"github.com/h0ek/gotify-vps-agent/internal/engine"
	"github.com/h0ek/gotify-vps-agent/internal/model"
	"github.com/h0ek/gotify-vps-agent/internal/queue"
	"github.com/h0ek/gotify-vps-agent/internal/state"
)

func TestUpgradeReadsLegacyRuntimeAndKeepsOfflineIncidentHistory(t *testing.T) {
	dir := t.TempDir()
	statePath, queuePath, cursorPath, configPath := filepath.Join(dir, "state.json"), filepath.Join(dir, "queue.json"), filepath.Join(dir, "journal.cursor"), filepath.Join(dir, "config.toml")
	files := map[string]string{
		statePath:  `{"version":1,"initialized":true,"last_run":"2026-08-26T12:00:00Z","checks":{"service.nginx":{"title":"Nginx service","current":"CRITICAL","last_observed":"CRITICAL","consecutive_failures":1,"consecutive_successes":0,"first_failure_at":"2026-08-26T12:00:00Z","last_notification_at":"2026-08-26T12:00:00Z","last_recovery_at":"0001-01-01T00:00:00Z","fingerprint":"legacy-state","message":"nginx.service is inactive","notified":true}}}`,
		queuePath:  `{"version":1,"events":[{"check_id":"service.nginx","title":"Nginx service","status":"CRITICAL","kind":"problem","message":"nginx.service is inactive","priority":10,"fingerprint":"legacy-event","created_at":"2026-08-26T12:00:00Z"}],"attempts":2,"last_attempt_at":"2026-08-26T12:05:00Z","next_attempt_at":"2026-08-26T12:15:00Z"}`,
		cursorPath: "s=legacy-cursor\n",
		configPath: "version = 1\n[gotify]\nurl = \"https://notify.example.com\"\nproxy_url = \"socks5h://127.0.0.1:9050\"\n[services]\nnginx = \"nginx.service\"\n",
	}
	for path, data := range files {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	current, err := state.Load(statePath)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := queue.Load(queuePath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Attempts != 2 || len(pending.Events) != 1 || cfg.Gotify.ProxyURL != "socks5h://127.0.0.1:9050" {
		t.Fatal("legacy configuration or retry state was lost")
	}
	for path, want := range files {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("reading legacy runtime changed %s: %v", path, err)
		}
	}
	now := time.Date(2026, 8, 26, 12, 10, 0, 0, time.UTC)
	result := model.Result{ID: "service.nginx", Title: "Nginx service", Status: model.StatusOK, Message: "nginx.service is active"}
	if events := engine.Evaluate(&current, []model.Result{result}, cfg, now); len(events) != 0 {
		t.Fatalf("one successful sample emitted recovery: %#v", events)
	}
	if err := state.Save(statePath, current); err != nil {
		t.Fatal(err)
	}
	current, err = state.Load(statePath)
	if err != nil {
		t.Fatal(err)
	}
	events := engine.Evaluate(&current, []model.Result{result}, cfg, now.Add(5*time.Minute))
	if len(events) != 1 || events[0].Kind != model.EventRecovery {
		t.Fatalf("legacy incident did not recover: %#v", events)
	}
	pending.Add(events, now.Add(5*time.Minute))
	if err := queue.Save(queuePath, pending); err != nil {
		t.Fatal(err)
	}
	loaded, err := queue.Load(queuePath)
	if err != nil || len(loaded.Events) != 2 || loaded.Events[0].Fingerprint != "legacy-event" || loaded.Events[1].Kind != model.EventRecovery {
		t.Fatalf("legacy incident history was lost after upgrade: %#v %v", loaded, err)
	}
	for _, path := range []string{cursorPath, configPath} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != files[path] {
			t.Fatalf("upgrade rewrote %s", path)
		}
	}
}

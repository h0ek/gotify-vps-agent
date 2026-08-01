package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/h0ek/gotify-vps-agent/internal/model"
)

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	value := New()
	value.Initialized = true
	value.LastRun = time.Now().UTC().Truncate(time.Second)
	value.Checks["disk"] = CheckState{Current: model.StatusWarning, LastObserved: model.StatusWarning, Notified: true}
	if err := Save(path, value); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Checks["disk"].Notified || !loaded.LastRun.Equal(value.LastRun) {
		t.Fatalf("unexpected loaded state: %#v", loaded)
	}
}

func TestStateRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"checks":{},"unexpected":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected strict JSON error, got %v", err)
	}
}

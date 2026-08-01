package systemd

import (
	"strings"
	"testing"
)

func TestTimerOverrideUsesInactiveAnchor(t *testing.T) {
	data, err := timerOverride("7m")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "OnUnitInactiveSec=7m") {
		t.Fatalf("unexpected override: %s", text)
	}
	if strings.Contains(text, "OnUnitActiveSec=7m") {
		t.Fatalf("legacy timer directive remains: %s", text)
	}
}

func TestTimerOverrideRejectsUnsafeValues(t *testing.T) {
	for _, value := range []string{"", "30s", "25h", "5m\nExecStart=/bin/sh"} {
		if _, err := timerOverride(value); err == nil {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
}

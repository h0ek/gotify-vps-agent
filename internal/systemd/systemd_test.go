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

func TestTimerHealthDuringExecutionAndBetweenRuns(t *testing.T) {
	base := "LoadState=loaded\nActiveState=active\nUnitFileState=enabled\n"
	cases := []struct {
		output string
		want   bool
	}{
		{base + "SubState=running\nNextElapseUSecRealtime=n/a\nNextElapseUSecMonotonic=infinity", true},
		{base + "SubState=waiting\nNextElapseUSecMonotonic=5min", true},
		{base + "SubState=waiting\nNextElapseUSecRealtime=0\nNextElapseUSecMonotonic=n/a", false},
		{base + "SubState=elapsed\nNextElapseUSecRealtime=infinity\nNextElapseUSecMonotonic=18446744073709551615", false},
		{strings.ReplaceAll(base, "enabled", "disabled") + "SubState=running", false},
		{strings.ReplaceAll(base, "active", "inactive") + "NextElapseUSecMonotonic=5min", false},
	}
	for _, test := range cases {
		if got := TimerHealthy(test.output); got != test.want {
			t.Fatalf("healthy=%t, want %t for %q", got, test.want, test.output)
		}
	}
}

package systemd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/h0ek/gotify-vps-agent/internal/runner"
	"github.com/h0ek/gotify-vps-agent/internal/securefile"
)

const timerOverridePath = "/etc/systemd/system/gotify-vps-agent.timer.d/override.conf"

func TimerHealthy(output string) bool {
	values := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	if values["LoadState"] != "loaded" || values["ActiveState"] != "active" || values["UnitFileState"] != "enabled" {
		return false
	}
	if values["SubState"] == "running" {
		return true
	}
	for _, property := range []string{"NextElapseUSecRealtime", "NextElapseUSecMonotonic"} {
		switch values[property] {
		case "", "0", "n/a", "infinity", "18446744073709551615":
		default:
			return true
		}
	}
	return false
}

func WriteTimerOverride(ctx context.Context, interval string) error {
	content, err := timerOverride(interval)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(timerOverridePath), 0755); err != nil {
		return err
	}
	if err := securefile.Write(timerOverridePath, content, 0644); err != nil {
		return err
	}
	result, err := runner.Run(ctx, 15*time.Second, "/usr/bin/systemctl", "daemon-reload")
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("systemctl daemon-reload failed: %s", result.Output)
	}
	return nil
}

func timerOverride(interval string) ([]byte, error) {
	duration, err := time.ParseDuration(interval)
	if err != nil || duration < time.Minute || duration > 24*time.Hour {
		return nil, fmt.Errorf("invalid timer interval %q", interval)
	}
	return []byte(fmt.Sprintf("[Timer]\nOnBootSec=\nOnUnitActiveSec=\nOnUnitInactiveSec=\nOnActiveSec=2m\nOnUnitInactiveSec=%s\n", interval)), nil
}

func EnableTimer(ctx context.Context) error {
	result, err := runner.Run(ctx, 20*time.Second, "/usr/bin/systemctl", "enable", "--now", "gotify-vps-agent.timer")
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("enable timer: %s", result.Output)
	}
	return nil
}

func DisableTimer(ctx context.Context) error {
	result, err := runner.Run(ctx, 20*time.Second, "/usr/bin/systemctl", "disable", "--now", "gotify-vps-agent.timer")
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("disable timer: %s", result.Output)
	}
	return nil
}

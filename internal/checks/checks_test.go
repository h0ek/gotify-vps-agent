package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/h0ek/gotify-vps-agent/internal/config"
	"github.com/h0ek/gotify-vps-agent/internal/model"
)

func TestParseJournalOutput(t *testing.T) {
	output, cursor := parseJournalOutput("line one\nline two\n-- cursor: s=abc123\n", "s=old")
	if output != "line one\nline two" {
		t.Fatalf("unexpected output: %q", output)
	}
	if cursor != "s=abc123" {
		t.Fatalf("unexpected cursor: %q", cursor)
	}
}

func TestParseJournalOutputKeepsExistingCursor(t *testing.T) {
	output, cursor := parseJournalOutput("", "s=old")
	if output != "" || cursor != "s=old" {
		t.Fatalf("unexpected result: output=%q cursor=%q", output, cursor)
	}
}

func TestValidateJournalCursorRejectsControlCharacters(t *testing.T) {
	if err := validateJournalCursor("s=abc\nnext"); err == nil {
		t.Fatal("expected control-character validation error")
	}
}

func TestValidateJournalCursorRejectsOversizedValue(t *testing.T) {
	value := make([]byte, 16*1024+1)
	for index := range value {
		value[index] = 'a'
	}
	if err := validateJournalCursor(string(value)); err == nil {
		t.Fatal("expected oversized-cursor validation error")
	}
}

func TestDeliveryQueueStatus(t *testing.T) {
	if result := DeliveryQueue(0, 0, time.Time{}); result.Status != model.StatusOK {
		t.Fatalf("unexpected empty queue status: %s", result.Status)
	}
	if result := DeliveryQueue(2, 1, time.Now()); result.Status != model.StatusWarning {
		t.Fatalf("unexpected queued status: %s", result.Status)
	}
	if result := DeliveryQueue(2, 5, time.Now()); result.Status != model.StatusCritical {
		t.Fatalf("unexpected retry status: %s", result.Status)
	}
}

func TestAgentFreshness(t *testing.T) {
	cfg := config.Default()
	cfg.Agent.Interval = "5m"
	if result := checkAgentFreshness(cfg, time.Now().Add(-10*time.Minute)); result.Status != model.StatusOK {
		t.Fatalf("unexpected fresh status: %s", result.Status)
	}
	if result := checkAgentFreshness(cfg, time.Now().Add(-20*time.Minute)); result.Status != model.StatusWarning {
		t.Fatalf("unexpected stale status: %s", result.Status)
	}
	if result := checkAgentFreshness(cfg, time.Now().Add(-40*time.Minute)); result.Status != model.StatusCritical {
		t.Fatalf("unexpected critical status: %s", result.Status)
	}
}

func TestNewestFileInfo(t *testing.T) {
	dir := t.TempDir()
	older := filepath.Join(dir, "older")
	newer := filepath.Join(dir, "newer")
	if err := os.WriteFile(older, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(older, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, now, now); err != nil {
		t.Fatal(err)
	}
	info, selected := newestFileInfo([]string{older, newer})
	if info == nil || selected != newer {
		t.Fatalf("unexpected selected stamp %q", selected)
	}
}

func TestParseMountInfoUsesHostMountOptions(t *testing.T) {
	input := strings.Join([]string{
		"20 1 8:1 / / rw,relatime - ext4 /dev/vda1 rw,errors=remount-ro",
		"21 20 8:2 / /srv ro,relatime - ext4 /dev/vda2 ro",
		"22 20 0:5 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw",
	}, "\n")
	mounts, err := parseMountInfo(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 2 {
		t.Fatalf("unexpected mount count: %d", len(mounts))
	}
	if mounts[0].Path != "/" || mounts[0].ReadOnly {
		t.Fatalf("unexpected root mount: %+v", mounts[0])
	}
	if mounts[1].Path != "/srv" || !mounts[1].ReadOnly {
		t.Fatalf("unexpected read-only mount: %+v", mounts[1])
	}
}

func TestCheckReadOnlyWithoutFilesystemsIsWarning(t *testing.T) {
	result := checkReadOnly(nil, nil)
	if result.Status != model.StatusWarning {
		t.Fatalf("unexpected status: %s", result.Status)
	}
}

func TestCheckReadOnlyReportsMountTableFailure(t *testing.T) {
	result := checkReadOnly(nil, os.ErrPermission)
	if result.Status != model.StatusWarning {
		t.Fatalf("unexpected status: %s", result.Status)
	}
}

func TestCheckReadOnlyReportsHostReadOnlyMount(t *testing.T) {
	result := checkReadOnly([]mount{{Path: "/", ReadOnly: false}, {Path: "/srv", ReadOnly: true}}, nil)
	if result.Status != model.StatusCritical || result.Message != "/srv" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

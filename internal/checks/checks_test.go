package checks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/h0ek/gotify-vps-agent/internal/config"
	"github.com/h0ek/gotify-vps-agent/internal/engine"
	"github.com/h0ek/gotify-vps-agent/internal/model"
	"github.com/h0ek/gotify-vps-agent/internal/runner"
	"github.com/h0ek/gotify-vps-agent/internal/state"
)

func TestParseJournalOutput(t *testing.T) {
	snapshot := parseJournalOutput(`{"__CURSOR":"s=first","__REALTIME_TIMESTAMP":"1000000","MESSAGE":"line one"}
{"__CURSOR":"s=second","__REALTIME_TIMESTAMP":"2000000","MESSAGE":"line two"}
-- cursor: s=second`, "s=old")
	if snapshot.Err != nil || snapshot.Cursor != "s=second" || len(snapshot.Entries) != 2 || !strings.Contains(snapshot.Output, "line two") {
		t.Fatalf("unexpected journal snapshot: %#v", snapshot)
	}
}

func TestParseJournalOutputKeepsExistingCursor(t *testing.T) {
	snapshot := parseJournalOutput("", "s=old")
	if snapshot.Err != nil || snapshot.Output != "" || snapshot.Cursor != "s=old" {
		t.Fatalf("unexpected empty journal snapshot: %#v", snapshot)
	}
}

func TestParseJournalOutputRejectsIncompleteRecords(t *testing.T) {
	for _, output := range []string{`{"MESSAGE":"event"}`, `{"__CURSOR":"s=1","MESSAGE":"event"}`, `{"__CURSOR":"s=1","__REALTIME_TIMESTAMP":"1000000",`, `not JSON`} {
		if snapshot := parseJournalOutput(output, "s=old"); snapshot.Err == nil || snapshot.Cursor != "" {
			t.Fatalf("incomplete output advanced cursor: %#v", snapshot)
		}
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

func TestFreshnessWarningSurvivesNextSuccessfulRun(t *testing.T) {
	cfg := config.Default()
	value := state.New()
	result := checkAgentFreshness(cfg, time.Now().Add(-20*time.Minute))
	events := engine.Evaluate(&value, []model.Result{result}, cfg, time.Now())
	if len(events) != 1 || events[0].Status != model.StatusWarning {
		t.Fatalf("one-off execution gap was suppressed: %#v", events)
	}
}

func TestSwapDoesNotReportMissingMemInfoAsHealthy(t *testing.T) {
	result := checkSwap(nil, config.Default())
	if result.Status != model.StatusWarning {
		t.Fatalf("missing swap data reported as healthy: %#v", result)
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

func TestJournalCollectorReducesOversizedPages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.cursor")
	calls := 0
	run := func(_ context.Context, _ time.Duration, _ string, args ...string) (runner.Result, error) {
		calls++
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--lines=+500") {
			return runner.Result{Truncated: true}, nil
		}
		return runner.Result{Output: `{"__CURSOR":"s=entry","__REALTIME_TIMESTAMP":"1000000","MESSAGE":"Out of memory: killed process 123"}`}, nil
	}
	snapshot := collectKernelJournalWithRunner(context.Background(), path, time.Now(), run)
	if snapshot.Err != nil || calls != 3 || snapshot.Cursor != "s=entry" || checkOOM(snapshot).Occurrence == "" {
		t.Fatalf("oversized page did not make progress: calls=%d snapshot=%#v", calls, snapshot)
	}
}

func TestJournalCollectorDoesNotAdvancePastOversizedEntry(t *testing.T) {
	run := func(context.Context, time.Duration, string, ...string) (runner.Result, error) {
		return runner.Result{Truncated: true}, nil
	}
	snapshot := collectKernelJournalWithRunner(context.Background(), filepath.Join(t.TempDir(), "journal.cursor"), time.Now(), run)
	if snapshot.Err == nil || snapshot.Cursor != "" {
		t.Fatalf("oversized entry was acknowledged: %#v", snapshot)
	}
}

func TestPostgreSQLRecoveryClusterIsOnline(t *testing.T) {
	for _, status := range []string{"online", "online,recovery"} {
		result := postgreSQLStatus("17 main 5432 " + status + " postgres /var/lib/postgresql/17/main /var/log/postgresql/postgresql-17-main.log")
		if result.Status != model.StatusOK {
			t.Fatalf("healthy cluster reported as failed: %#v", result)
		}
	}
	if result := postgreSQLStatus("17 main 5432 down postgres /data /log"); result.Status != model.StatusCritical {
		t.Fatalf("offline cluster reported as healthy: %#v", result)
	}
}

func TestInvalidMemoryDataIsWarning(t *testing.T) {
	for _, values := range []map[string]uint64{nil, {"MemTotal": 1024}, {"MemTotal": 1024, "MemAvailable": 2048}} {
		if result := checkMemory(values, config.Default()); result.Status != model.StatusWarning {
			t.Fatalf("invalid memory data was accepted: %#v", result)
		}
	}
}

func TestInaccessibleFilesystemDoesNotBecomeHealthy(t *testing.T) {
	values := []filesystemUsage{{Mount: mount{Path: "/"}, DiskUsed: 20, InodeUsed: 10}, {Mount: mount{Path: "/home"}, Err: os.ErrPermission}}
	cfg := config.Default()
	for _, result := range []model.Result{checkDisk(values, cfg), checkInode(values, cfg)} {
		if result.Status != model.StatusWarning || !strings.Contains(result.Message, "/home could not be inspected") {
			t.Fatalf("inaccessible filesystem was silently skipped: %#v", result)
		}
	}
	values[0].DiskUsed = 99
	values[0].InodeUsed = 99
	for _, result := range []model.Result{checkDisk(values, cfg), checkInode(values, cfg)} {
		if result.Status != model.StatusCritical {
			t.Fatalf("inspection failure masked a full filesystem: %#v", result)
		}
	}
}

func TestInspectFilesystemsReadsHostMountsAndReportsFailures(t *testing.T) {
	values := inspectFilesystems([]mount{{Path: "/"}, {Path: "/gotify-vps-agent-test-nonexistent-mount"}})
	if len(values) != 2 {
		t.Fatalf("filesystem inspection silently dropped a mount: %#v", values)
	}
	for _, value := range values {
		if value.Mount.Path == "/" && value.Err != nil && !errors.Is(value.Err, os.ErrPermission) {
			t.Fatalf("host root filesystem could not be inspected: %v", value.Err)
		}
		if value.Mount.Path != "/" && value.Err == nil {
			t.Fatal("missing mount did not return an inspection failure")
		}
	}
}

func FuzzParseJournalOutput(f *testing.F) {
	f.Add(`{"__CURSOR":"s=entry","__REALTIME_TIMESTAMP":"1000000","MESSAGE":"Out of memory"}`)
	f.Add(`{"__CURSOR":"s=binary","__REALTIME_TIMESTAMP":"1000000","MESSAGE":[65,66,255]}`)
	f.Add("")
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 1024*1024 {
			t.Skip()
		}
		_ = parseJournalOutput(value, "s=previous")
	})
}

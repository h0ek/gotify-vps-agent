package checks

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/h0ek/gotify-vps-agent/internal/config"
	"github.com/h0ek/gotify-vps-agent/internal/engine"
	"github.com/h0ek/gotify-vps-agent/internal/model"
	"github.com/h0ek/gotify-vps-agent/internal/queue"
	"github.com/h0ek/gotify-vps-agent/internal/runner"
	"github.com/h0ek/gotify-vps-agent/internal/state"
)

func journalTools(t *testing.T) (string, string) {
	t.Helper()
	journal := os.Getenv("GVA_TEST_JOURNALCTL")
	if journal == "" {
		journal, _ = exec.LookPath("journalctl")
	}
	remote := os.Getenv("GVA_TEST_JOURNAL_REMOTE")
	if remote == "" {
		for _, path := range []string{"/usr/lib/systemd/systemd-journal-remote", "/lib/systemd/systemd-journal-remote"} {
			if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0111 != 0 {
				remote = path
				break
			}
		}
	}
	if journal == "" || remote == "" {
		if os.Getenv("GVA_REQUIRE_JOURNAL_INTEGRATION") == "1" {
			t.Fatal("journal integration tools are required")
		}
		t.Skip("journalctl and systemd-journal-remote are required")
	}
	return journal, remote
}

func journalFixture(t *testing.T, messages []string) (string, string, time.Time) {
	t.Helper()
	journal, remote := journalTools(t)
	start := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	var exported strings.Builder
	for index, message := range messages {
		fmt.Fprintf(&exported, "__REALTIME_TIMESTAMP=%d\n__MONOTONIC_TIMESTAMP=%d\n_BOOT_ID=11223344556677889900aabbccddeeff\n_MACHINE_ID=00112233445566778899aabbccddeeff\n_TRANSPORT=kernel\nPRIORITY=3\nMESSAGE=%s\n\n", start.UnixMicro()+int64(index)*1000000, int64(index)*1000000, message)
	}
	path := filepath.Join(t.TempDir(), "fixture.journal")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, remote, "--output="+path, "--split-mode=none", "--compress=no", "-")
	command.Stdin = strings.NewReader(exported.String())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("journal fixture import failed: %v\n%s", err, output)
	}
	return journal, path, start
}

func journalRunner(journal, path string) func(context.Context, time.Duration, string, ...string) (runner.Result, error) {
	return func(ctx context.Context, timeout time.Duration, _ string, args ...string) (runner.Result, error) {
		return runner.Run(ctx, timeout, journal, append([]string{"--file=" + path}, args...)...)
	}
}

func TestJournalIntegrationCursorAndSinceBacklogs(t *testing.T) {
	messages := make([]string, 2505)
	for index := range messages {
		messages[index] = fmt.Sprintf("record-%04d", index)
	}
	messages[1] += " Out of memory: Killed process 123"
	journal, path, start := journalFixture(t, messages)
	run := journalRunner(journal, path)
	read := func(args ...string) journalSnapshot {
		t.Helper()
		result, err := run(context.Background(), 15*time.Second, "", append([]string{"--quiet", "--no-pager", "--output=json"}, args...)...)
		if err != nil || result.ExitCode != 0 || result.Truncated {
			t.Fatalf("journalctl failed: %v, %#v", err, result)
		}
		value := parseJournalOutput(result.Output, "")
		if value.Err != nil {
			t.Fatal(value.Err)
		}
		return value
	}
	first := read("--lines=+1")
	for _, limit := range []string{"--lines=2000", "--lines=+2000"} {
		value := read(limit, "--after-cursor="+first.Cursor)
		if len(value.Entries) != 2000 || checkOOM(value).Status != model.StatusCritical || !strings.Contains(value.Entries[len(value.Entries)-1].Line, "record-2000") {
			t.Fatalf("cursor pagination lost the first OOM with %s", limit)
		}
	}
	tail := read("--lines=2000", "--since", "@"+fmt.Sprint(start.Unix()))
	if !strings.Contains(tail.Entries[0].Line, "record-0505") || checkOOM(tail).Status != model.StatusOK {
		t.Fatal("unexpected since-only tail semantics")
	}
	cursorPath := filepath.Join(t.TempDir(), "cursor")
	if err := os.WriteFile(cursorPath, []byte(first.Cursor), 0600); err != nil {
		t.Fatal(err)
	}
	value := collectKernelJournalWithRunner(context.Background(), cursorPath, start, run)
	if value.Err != nil || len(value.Entries) != 2000 || checkOOM(value).Occurrence == "" {
		t.Fatalf("first collector page failed: %#v", value)
	}
	if err := os.WriteFile(cursorPath, []byte(value.Cursor), 0600); err != nil {
		t.Fatal(err)
	}
	value = collectKernelJournalWithRunner(context.Background(), cursorPath, start, run)
	if value.Err != nil || len(value.Entries) != 504 || !strings.Contains(value.Output, "record-2504") {
		t.Fatalf("remaining collector backlog was lost: %#v", value)
	}
	if err := os.WriteFile(cursorPath, []byte("invalid-cursor"), 0600); err != nil {
		t.Fatal(err)
	}
	value = collectKernelJournalWithRunner(context.Background(), cursorPath, start.Add(-time.Second), run)
	if value.Err != nil || len(value.Entries) != 2000 || checkOOM(value).Occurrence == "" {
		t.Fatalf("invalid cursor fallback lost early records: %#v", value)
	}
}

func TestJournalIntegrationByteLimitMakesProgress(t *testing.T) {
	messages := make([]string, 260)
	for index := range messages {
		messages[index] = fmt.Sprintf("large-%04d ", index) + strings.Repeat("x", 8192)
	}
	messages[0] = "Out of memory: Killed process 123 " + messages[0]
	journal, path, start := journalFixture(t, messages)
	run := journalRunner(journal, path)
	cursorPath := filepath.Join(t.TempDir(), "cursor")
	seen := make(map[string]bool)
	pages := 0
	for len(seen) < len(messages) {
		value := collectKernelJournalWithRunner(context.Background(), cursorPath, start.Add(-time.Second), run)
		if value.Err != nil || len(value.Entries) == 0 {
			t.Fatalf("byte-limited journal page stalled: %v", value.Err)
		}
		if pages == 0 && checkOOM(value).Status != model.StatusCritical {
			t.Fatal("byte limiting skipped the first OOM")
		}
		for _, entry := range value.Entries {
			if seen[entry.Cursor] {
				t.Fatal("journal page repeated an acknowledged entry")
			}
			seen[entry.Cursor] = true
		}
		if err := os.WriteFile(cursorPath, []byte(value.Cursor), 0600); err != nil {
			t.Fatal(err)
		}
		pages++
	}
	if pages < 2 || len(seen) != len(messages) {
		t.Fatalf("unexpected byte pagination result: pages=%d entries=%d", pages, len(seen))
	}
}

func TestJournalIntegrationIndependentIncidentsSurviveRestart(t *testing.T) {
	journal, path, start := journalFixture(t, []string{"Out of memory: Killed process 123", "Out of memory: Killed process 123", "buffer I/O error on vda", "buffer I/O error on vda"})
	baseRun := journalRunner(journal, path)
	run := func(ctx context.Context, timeout time.Duration, path string, args ...string) (runner.Result, error) {
		for index, arg := range args {
			if strings.HasPrefix(arg, "--lines=") {
				args[index] = "--lines=+1"
			}
		}
		return baseRun(ctx, timeout, path, args...)
	}
	dir := t.TempDir()
	cursorPath, statePath, queuePath := filepath.Join(dir, "cursor"), filepath.Join(dir, "state.json"), filepath.Join(dir, "queue.json")
	current, pending := state.New(), queue.New()
	for index := 0; index < 4; index++ {
		value := collectKernelJournalWithRunner(context.Background(), cursorPath, start.Add(-time.Second), run)
		if value.Err != nil || len(value.Entries) != 1 {
			t.Fatalf("incident journal read failed: %#v", value)
		}
		now := start.Add(time.Duration(index+1) * time.Minute)
		events := engine.Evaluate(&current, []model.Result{checkOOM(value), checkKernelErrors(value)}, config.Default(), now)
		if len(events) != 1 || events[0].Kind != model.EventOccurrence {
			t.Fatalf("independent incident was suppressed: %#v", events)
		}
		pending.Add(events, now)
		if err := state.Save(statePath, current); err != nil {
			t.Fatal(err)
		}
		if err := queue.Save(queuePath, pending); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cursorPath, []byte(value.Cursor), 0600); err != nil {
			t.Fatal(err)
		}
		var err error
		if current, err = state.Load(statePath); err != nil {
			t.Fatal(err)
		}
		if pending, err = queue.Load(queuePath); err != nil {
			t.Fatal(err)
		}
	}
	if len(pending.Events) != 4 {
		t.Fatalf("incident queue lost history: %#v", pending.Events)
	}
}

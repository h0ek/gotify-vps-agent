package queue

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/h0ek/gotify-vps-agent/internal/model"
)

func TestQueueDeduplicatesAndBoundsEvents(t *testing.T) {
	q := New()
	now := time.Now().UTC()
	event := model.Event{CheckID: "disk", Fingerprint: "same"}
	q.Add([]model.Event{event, event}, now)
	if len(q.Events) != 1 {
		t.Fatalf("expected one event, got %d", len(q.Events))
	}
	many := make([]model.Event, 0, maxEvents+20)
	for i := 0; i < maxEvents+20; i++ {
		many = append(many, model.Event{CheckID: fmt.Sprintf("check-%d", i), Fingerprint: fmt.Sprintf("fingerprint-%d", i)})
	}
	q.Add(many, now)
	if len(q.Events) != maxEvents {
		t.Fatalf("expected %d events, got %d", maxEvents, len(q.Events))
	}
}

func TestQueueBackoff(t *testing.T) {
	q := New()
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	q.Add([]model.Event{{CheckID: "disk", Fingerprint: "disk-1"}}, now)
	if !q.Ready(now) {
		t.Fatal("new events should be ready immediately")
	}
	q.MarkFailure(now)
	if q.Ready(now.Add(4 * time.Minute)) {
		t.Fatal("queue should remain in backoff")
	}
	if !q.Ready(now.Add(5 * time.Minute)) {
		t.Fatal("queue should be ready after first backoff")
	}
	q.MarkFailure(now.Add(5 * time.Minute))
	if !q.NextAttemptAt.Equal(now.Add(15 * time.Minute)) {
		t.Fatalf("unexpected second retry time: %s", q.NextAttemptAt)
	}
	q.Add([]model.Event{{CheckID: "memory", Fingerprint: "memory-1"}}, now.Add(6*time.Minute))
	if !q.Ready(now.Add(6*time.Minute)) || q.Attempts != 0 {
		t.Fatal("a new event should bypass an existing backoff")
	}
}

func TestQueuePreservesProblemAndRecovery(t *testing.T) {
	q := New()
	now := time.Now().UTC()
	q.Add([]model.Event{{CheckID: "disk", Fingerprint: "problem", Kind: model.EventProblem}}, now)
	q.MarkFailure(now)
	q.Add([]model.Event{{CheckID: "disk", Fingerprint: "recovery", Kind: model.EventRecovery}}, now.Add(time.Minute))
	if len(q.Events) != 2 || q.Events[0].Fingerprint != "problem" || q.Events[1].Fingerprint != "recovery" {
		t.Fatalf("incident history was lost: %#v", q.Events)
	}
	if q.Attempts != 0 {
		t.Fatal("new transition should reset delivery backoff")
	}
}

func TestQueueRemovesDisabledChecks(t *testing.T) {
	q := New()
	q.Add([]model.Event{{CheckID: "service.nginx", Fingerprint: "nginx"}, {CheckID: "filesystem.disk", Fingerprint: "disk"}}, time.Now())
	q.MarkFailure(time.Now())
	q.RetainChecks([]model.Result{{ID: "filesystem.disk"}})
	if len(q.Events) != 1 || q.Events[0].CheckID != "filesystem.disk" || q.Attempts != 1 {
		t.Fatalf("incorrect active queue: %#v", q)
	}
	q.RetainChecks(nil)
	if len(q.Events) != 0 || q.Attempts != 0 || !q.NextAttemptAt.IsZero() {
		t.Fatalf("empty queue retained retry metadata: %#v", q)
	}
}

func TestQueueSizeLimitIncludesJSONEscapingAndFormatting(t *testing.T) {
	q := New()
	for index := 0; index < maxEvents; index++ {
		q.Add([]model.Event{{CheckID: fmt.Sprintf("check-%d", index), Fingerprint: fmt.Sprintf("event-%d", index), Message: strings.Repeat("&", 1700)}}, time.Now())
	}
	path := filepath.Join(t.TempDir(), "queue.json")
	if err := Save(path, q); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() > maxBytes {
		t.Fatalf("saved queue exceeded its limit: %v, %v", info, err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
}

func TestIncidentCyclesAndOccurrencesSurviveReload(t *testing.T) {
	q := New()
	now := time.Now().UTC()
	problem := model.Event{CheckID: "nginx", Kind: model.EventProblem, Status: model.StatusCritical, Fingerprint: "same-problem"}
	recovery := model.Event{CheckID: "nginx", Kind: model.EventRecovery, Status: model.StatusOK, Fingerprint: "same-recovery"}
	for index, event := range []model.Event{problem, recovery, problem, recovery, {CheckID: "memory.oom", Kind: model.EventOccurrence, Status: model.StatusCritical, Fingerprint: "oom-1"}, {CheckID: "memory.oom", Kind: model.EventOccurrence, Status: model.StatusCritical, Fingerprint: "oom-2"}} {
		q.Add([]model.Event{event}, now.Add(time.Duration(index)*time.Minute))
	}
	q.Add([]model.Event{{CheckID: "memory.oom", Kind: model.EventOccurrence, Status: model.StatusCritical, Fingerprint: "oom-1"}}, now.Add(time.Hour))
	path := filepath.Join(t.TempDir(), "queue.json")
	if err := Save(path, q); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || len(loaded.Events) != 6 {
		t.Fatalf("incident history did not survive restart: %#v %v", loaded, err)
	}
	for index, event := range loaded.Events {
		if !event.CreatedAt.Equal(now.Add(time.Duration(index) * time.Minute)) {
			t.Fatalf("incident %d lost observation time", index)
		}
	}
}

func TestRemindersDoNotFillPendingIncidentHistory(t *testing.T) {
	q := New()
	now := time.Now().UTC()
	q.Add([]model.Event{{CheckID: "disk", Kind: model.EventProblem, Status: model.StatusWarning, Fingerprint: "first"}}, now)
	for index := 0; index < 20; index++ {
		q.Add([]model.Event{{CheckID: "disk", Kind: model.EventReminder, Status: model.StatusWarning, Fingerprint: fmt.Sprint(index)}}, now.Add(time.Duration(index+1)*time.Hour))
	}
	if len(q.Events) != 1 || !q.Events[0].CreatedAt.Equal(now) {
		t.Fatalf("reminders replaced incident age or filled queue: %#v", q)
	}
}

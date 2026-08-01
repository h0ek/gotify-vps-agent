package queue

import (
	"fmt"
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

func TestQueueKeepsOnlyLatestEventPerCheck(t *testing.T) {
	q := New()
	now := time.Now().UTC()
	q.Add([]model.Event{{CheckID: "disk", Fingerprint: "problem", Kind: model.EventProblem}}, now)
	q.MarkFailure(now)
	q.Add([]model.Event{{CheckID: "disk", Fingerprint: "recovery", Kind: model.EventRecovery}}, now.Add(time.Minute))
	if len(q.Events) != 1 || q.Events[0].Fingerprint != "recovery" {
		t.Fatalf("stale event remained in queue: %#v", q.Events)
	}
	if q.Attempts != 0 {
		t.Fatal("new transition should reset delivery backoff")
	}
}

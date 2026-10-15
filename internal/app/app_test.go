package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/h0ek/gotify-vps-agent/internal/gotify"
	"github.com/h0ek/gotify-vps-agent/internal/model"
	"github.com/h0ek/gotify-vps-agent/internal/queue"
)

func TestAcquireLockExcludesConcurrentProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.lock")
	first, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := acquireLock(path)
	if err == nil {
		second.Close()
		t.Fatal("expected second lock acquisition to fail")
	}
}

func TestDeliveryBatchesPersistOnlyUnsentEvents(t *testing.T) {
	now := time.Now().UTC()
	pending := queue.New()
	for index := 0; index < 10; index++ {
		pending.Add([]model.Event{{CheckID: fmt.Sprintf("check-%d", index), Title: fmt.Sprintf("Check %d", index), Message: strings.Repeat("&", 4096), Status: model.StatusCritical, Kind: model.EventProblem, Priority: 10, Fingerprint: fmt.Sprintf("event-%d", index)}}, now)
	}
	path := filepath.Join(t.TempDir(), "queue.json")
	save := func(value queue.Queue) error { return queue.Save(path, value) }
	attempts := 0
	delivered := ""
	send := func(_ context.Context, _ string, body string, _ int) error {
		attempts++
		if len(body) > gotify.MaxMessageBytes {
			t.Fatalf("oversized batch: %d bytes", len(body))
		}
		if attempts == 2 {
			return errors.New("temporary delivery failure")
		}
		delivered += body
		return nil
	}
	if err := deliverQueued(context.Background(), &pending, "host", now, send, save, io.Discard); err != nil {
		t.Fatal(err)
	}
	persisted, err := queue.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Events) == 0 || len(persisted.Events) >= 10 || persisted.Attempts != 1 {
		t.Fatalf("wrong queue after a partial delivery: %#v", persisted)
	}
	if err := deliverQueued(context.Background(), &persisted, "host", now.Add(5*time.Minute), send, save, io.Discard); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 10; index++ {
		if strings.Count(delivered, fmt.Sprintf("**Check %d**", index)) != 1 {
			t.Fatalf("check %d was lost or delivered twice", index)
		}
	}
	finished, err := queue.Load(path)
	if err != nil || len(finished.Events) != 0 || finished.Attempts != 0 {
		t.Fatalf("queue was not cleared: %#v, %v", finished, err)
	}
}

func TestDeliveryStopsWhenAcknowledgementCannotBeSaved(t *testing.T) {
	pending := queue.New()
	pending.Add([]model.Event{{CheckID: "disk", Fingerprint: "disk", Status: model.StatusWarning}}, time.Now())
	want := errors.New("disk write failed")
	calls := 0
	err := deliverQueued(context.Background(), &pending, "host", time.Now(), func(context.Context, string, string, int) error {
		calls++
		return nil
	}, func(queue.Queue) error { return want }, io.Discard)
	if !errors.Is(err, want) || calls != 1 {
		t.Fatalf("unexpected delivery result: calls=%d err=%v", calls, err)
	}
}

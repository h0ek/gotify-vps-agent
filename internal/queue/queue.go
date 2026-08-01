package queue

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/h0ek/gotify-vps-agent/internal/model"
	"github.com/h0ek/gotify-vps-agent/internal/securefile"
	"github.com/h0ek/gotify-vps-agent/internal/textsafe"
)

const (
	maxEvents = 100
	maxBytes  = 1024 * 1024
)

type Queue struct {
	Version       int       `json:"version"`
	Events        []Event   `json:"events"`
	Attempts      int       `json:"attempts,omitempty"`
	LastAttemptAt time.Time `json:"last_attempt_at,omitempty"`
	NextAttemptAt time.Time `json:"next_attempt_at,omitempty"`
}

type Event struct {
	model.Event
	CreatedAt time.Time `json:"created_at"`
}

func New() Queue {
	return Queue{Version: 1, Events: []Event{}}
}

func Load(path string) (Queue, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return Queue{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return Queue{}, err
	}
	if len(data) > maxBytes {
		return Queue{}, fmt.Errorf("queue file exceeds %d bytes", maxBytes)
	}
	var value Queue
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return Queue{}, fmt.Errorf("parse queue: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Queue{}, fmt.Errorf("parse queue: trailing JSON data")
	}
	if value.Version != 1 {
		return Queue{}, fmt.Errorf("unsupported queue version %d", value.Version)
	}
	if len(value.Events) > maxEvents || value.Attempts < 0 {
		return Queue{}, fmt.Errorf("queue metadata is invalid")
	}
	seen := map[string]bool{}
	for index := range value.Events {
		event := &value.Events[index]
		if event.CheckID == "" || len(event.CheckID) > 128 || event.Fingerprint == "" || len(event.Fingerprint) > 128 {
			return Queue{}, fmt.Errorf("queue contains an invalid event")
		}
		if seen[event.CheckID] {
			return Queue{}, fmt.Errorf("queue contains duplicate check IDs")
		}
		seen[event.CheckID] = true
		event.Title = textsafe.Sanitize(event.Title, 256)
		event.Message = textsafe.Sanitize(event.Message, 4096)
	}
	return value, nil
}

func (q *Queue) Add(events []model.Event, now time.Time) bool {
	changed := false
	for _, incoming := range events {
		if incoming.CheckID == "" || incoming.Fingerprint == "" {
			continue
		}
		filtered := q.Events[:0]
		identical := false
		for _, existing := range q.Events {
			if existing.CheckID != incoming.CheckID {
				filtered = append(filtered, existing)
				continue
			}
			if existing.Fingerprint == incoming.Fingerprint {
				filtered = append(filtered, existing)
				identical = true
			}
		}
		q.Events = filtered
		if identical {
			continue
		}
		q.Events = append(q.Events, Event{Event: incoming, CreatedAt: now})
		changed = true
	}
	if changed {
		q.Attempts = 0
		q.NextAttemptAt = time.Time{}
	}
	q.trim()
	return changed
}

func (q Queue) Ready(now time.Time) bool {
	return len(q.Events) > 0 && (q.NextAttemptAt.IsZero() || !now.Before(q.NextAttemptAt))
}

func (q *Queue) MarkFailure(now time.Time) {
	q.Attempts++
	q.LastAttemptAt = now
	delay := 5 * time.Minute
	for i := 1; i < q.Attempts && delay < 6*time.Hour; i++ {
		delay *= 2
	}
	if delay > 6*time.Hour {
		delay = 6 * time.Hour
	}
	q.NextAttemptAt = now.Add(delay)
}

func (q *Queue) Clear() {
	q.Events = []Event{}
	q.Attempts = 0
	q.LastAttemptAt = time.Time{}
	q.NextAttemptAt = time.Time{}
}

func (q *Queue) trim() {
	if len(q.Events) > maxEvents {
		q.Events = q.Events[len(q.Events)-maxEvents:]
	}
	for len(q.Events) > 0 {
		data, _ := json.Marshal(q)
		if len(data) <= maxBytes {
			break
		}
		q.Events = q.Events[1:]
	}
}

func Save(path string, value Queue) error {
	value.Version = 1
	value.trim()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > maxBytes {
		return fmt.Errorf("queue file would exceed %d bytes", maxBytes)
	}
	return securefile.Write(path, data, 0600)
}

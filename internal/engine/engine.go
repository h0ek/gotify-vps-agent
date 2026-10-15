package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/h0ek/gotify-vps-agent/internal/config"
	"github.com/h0ek/gotify-vps-agent/internal/model"
	"github.com/h0ek/gotify-vps-agent/internal/state"
)

func Baseline(current *state.State, results []model.Result, now time.Time) {
	current.Checks = map[string]state.CheckState{}
	for _, result := range results {
		check := state.CheckState{
			Title:        result.Title,
			Current:      result.Status,
			LastObserved: result.Status,
			Fingerprint:  result.Fingerprint(),
			Message:      result.Message,
		}
		if result.Status == model.StatusOK {
			check.ConsecutiveSuccesses = 1
		} else {
			check.ConsecutiveFailures = 1
			check.FirstFailureAt = now
		}
		current.Checks[result.ID] = check
	}
	current.Initialized = true
	current.LastRun = now
}

func Evaluate(current *state.State, results []model.Result, cfg config.Config, now time.Time) []model.Event {
	if current.Checks == nil {
		current.Checks = map[string]state.CheckState{}
	}
	active := make(map[string]bool, len(results))
	for _, result := range results {
		active[result.ID] = true
	}
	for id := range current.Checks {
		if !active[id] {
			delete(current.Checks, id)
		}
	}
	events := make([]model.Event, 0)
	for _, result := range results {
		check, exists := current.Checks[result.ID]
		if !exists {
			check = state.CheckState{Title: result.Title, Current: model.StatusOK, LastObserved: model.StatusOK}
		}
		check.Title = result.Title
		check.Message = result.Message
		previousFingerprint := check.Fingerprint
		journalCheck := result.ID == "memory.oom" || result.ID == "kernel.errors"
		if !journalCheck || result.Occurrence != "" {
			check.Fingerprint = result.Fingerprint()
		}
		if result.Occurrence != "" {
			if previousFingerprint != check.Fingerprint {
				events = append(events, newEvent(result, model.EventOccurrence, result.Status, result.Message))
				check.LastNotificationAt = now
			}
			check.Current = result.Status
			check.LastObserved = result.Status
			check.ConsecutiveFailures = 1
			check.ConsecutiveSuccesses = 0
			check.Notified = true
			current.Checks[result.ID] = check
			continue
		}
		if journalCheck && check.Current == model.StatusCritical {
			check.Current = model.StatusOK
			check.LastObserved = model.StatusOK
			check.Notified = false
			check.ConsecutiveFailures = 0
		}

		if result.Status == model.StatusOK {
			if check.LastObserved == model.StatusOK {
				check.ConsecutiveSuccesses++
			} else {
				check.ConsecutiveSuccesses = 1
			}
			check.ConsecutiveFailures = 0
			check.FirstFailureAt = time.Time{}
			if check.Current != model.StatusOK && check.Notified && check.ConsecutiveSuccesses >= cfg.Agent.RecoverySuccesses {
				if cfg.Agent.NotifyRecovery {
					events = append(events, newEvent(result, model.EventRecovery, model.StatusOK, fmt.Sprintf("Recovered: %s", result.Message)))
					check.LastNotificationAt = now
				}
				check.Current = model.StatusOK
				check.LastRecoveryAt = now
				check.Notified = false
			} else if check.Current != model.StatusOK && !check.Notified && check.ConsecutiveSuccesses >= cfg.Agent.RecoverySuccesses {
				check.Current = model.StatusOK
			}
			check.LastObserved = model.StatusOK
			current.Checks[result.ID] = check
			continue
		}

		if check.LastObserved != model.StatusOK && (!check.Notified || check.LastObserved == result.Status) {
			check.ConsecutiveFailures++
		} else {
			check.ConsecutiveFailures = 1
		}
		if check.FirstFailureAt.IsZero() {
			check.FirstFailureAt = now
		}
		check.ConsecutiveSuccesses = 0
		check.LastObserved = result.Status

		required := cfg.Agent.WarningFailures
		if result.Immediate {
			required = 1
		}

		switch {
		case !check.Notified && check.ConsecutiveFailures >= required:
			event := newEvent(result, model.EventProblem, result.Status, result.Message)
			event.Fingerprint += fmt.Sprintf("-%d", check.FirstFailureAt.UnixNano())
			events = append(events, event)
			check.Current = result.Status
			check.LastNotificationAt = now
			check.Notified = true
		case check.Current == model.StatusWarning && result.Status == model.StatusCritical && check.ConsecutiveFailures >= required:
			events = append(events, newEvent(result, model.EventEscalation, result.Status, result.Message))
			check.Current = model.StatusCritical
			check.LastNotificationAt = now
			check.Notified = true
		case check.Current == result.Status && check.Notified && reminderDue(check, cfg, now):
			events = append(events, newEvent(result, model.EventReminder, result.Status, result.Message))
			check.LastNotificationAt = now
		}
		current.Checks[result.ID] = check
	}
	current.Initialized = true
	current.LastRun = now
	return events
}

func reminderDue(check state.CheckState, cfg config.Config, now time.Time) bool {
	if check.LastNotificationAt.IsZero() {
		return false
	}
	hours := cfg.Agent.WarningReminderHours
	if check.Current == model.StatusCritical {
		hours = cfg.Agent.CriticalReminderHours
	}
	if hours == 0 {
		return false
	}
	return now.Sub(check.LastNotificationAt) >= time.Duration(hours)*time.Hour
}

func newEvent(result model.Result, kind model.EventKind, status model.Status, message string) model.Event {
	fingerprintValue := fmt.Sprintf("%s\x00%s\x00%s\x00%s", result.ID, kind, status, message)
	if result.Occurrence != "" {
		fingerprintValue += "\x00" + result.Occurrence
	}
	sum := sha256.Sum256([]byte(fingerprintValue))
	return model.Event{
		CheckID:     result.ID,
		Title:       result.Title,
		Status:      status,
		Kind:        kind,
		Message:     message,
		Priority:    model.Priority(status, kind),
		Fingerprint: hex.EncodeToString(sum[:]),
	}
}

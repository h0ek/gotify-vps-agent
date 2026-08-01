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
	if current.Checks == nil {
		current.Checks = map[string]state.CheckState{}
	}
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
	events := make([]model.Event, 0)
	for _, result := range results {
		check, exists := current.Checks[result.ID]
		if !exists {
			check = state.CheckState{Title: result.Title, Current: model.StatusOK, LastObserved: model.StatusOK}
		}
		check.Title = result.Title
		check.Message = result.Message
		check.Fingerprint = result.Fingerprint()

		if result.Status == model.StatusOK {
			if check.LastObserved == model.StatusOK {
				check.ConsecutiveSuccesses++
			} else {
				check.ConsecutiveSuccesses = 1
			}
			check.ConsecutiveFailures = 0
			check.FirstFailureAt = time.Time{}
			if check.Current != model.StatusOK && check.Notified && check.ConsecutiveSuccesses >= cfg.Agent.RecoverySuccesses {
				events = append(events, newEvent(result, model.EventRecovery, model.StatusOK, fmt.Sprintf("Recovered: %s", result.Message)))
				check.Current = model.StatusOK
				check.LastNotificationAt = now
				check.LastRecoveryAt = now
				check.Notified = false
			} else if check.Current != model.StatusOK && !check.Notified && check.ConsecutiveSuccesses >= cfg.Agent.RecoverySuccesses {
				check.Current = model.StatusOK
			}
			check.LastObserved = model.StatusOK
			current.Checks[result.ID] = check
			continue
		}

		if check.LastObserved == result.Status {
			check.ConsecutiveFailures++
		} else {
			check.ConsecutiveFailures = 1
			check.FirstFailureAt = now
		}
		check.ConsecutiveSuccesses = 0
		check.LastObserved = result.Status

		required := cfg.Agent.WarningFailures
		if result.Status == model.StatusCritical && result.Immediate {
			required = 1
		}

		switch {
		case check.Current == model.StatusOK && check.ConsecutiveFailures >= required:
			events = append(events, newEvent(result, model.EventProblem, result.Status, result.Message))
			check.Current = result.Status
			check.LastNotificationAt = now
			check.Notified = true
		case check.Current == model.StatusWarning && result.Status == model.StatusCritical && check.ConsecutiveFailures >= required:
			events = append(events, newEvent(result, model.EventEscalation, result.Status, result.Message))
			check.Current = model.StatusCritical
			check.LastNotificationAt = now
			check.Notified = true
		case check.Current == model.StatusCritical && result.Status == model.StatusWarning && check.ConsecutiveFailures >= cfg.Agent.WarningFailures:
			check.Current = model.StatusWarning
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
	return now.Sub(check.LastNotificationAt) >= time.Duration(hours)*time.Hour
}

func newEvent(result model.Result, kind model.EventKind, status model.Status, message string) model.Event {
	fingerprintValue := fmt.Sprintf("%s\x00%s\x00%s\x00%s", result.ID, kind, status, message)
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

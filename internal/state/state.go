package state

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

const maxStateBytes = 2 * 1024 * 1024

type State struct {
	Version     int                   `json:"version"`
	Initialized bool                  `json:"initialized"`
	LastRun     time.Time             `json:"last_run,omitempty"`
	Checks      map[string]CheckState `json:"checks"`
}

type CheckState struct {
	Title                string       `json:"title"`
	Current              model.Status `json:"current"`
	LastObserved         model.Status `json:"last_observed"`
	ConsecutiveFailures  int          `json:"consecutive_failures"`
	ConsecutiveSuccesses int          `json:"consecutive_successes"`
	FirstFailureAt       time.Time    `json:"first_failure_at,omitempty"`
	LastNotificationAt   time.Time    `json:"last_notification_at,omitempty"`
	LastRecoveryAt       time.Time    `json:"last_recovery_at,omitempty"`
	Fingerprint          string       `json:"fingerprint,omitempty"`
	Message              string       `json:"message,omitempty"`
	Notified             bool         `json:"notified,omitempty"`
}

func New() State {
	return State{Version: 1, Checks: map[string]CheckState{}}
}

func Load(path string) (State, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return State{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxStateBytes+1))
	if err != nil {
		return State{}, err
	}
	if len(data) > maxStateBytes {
		return State{}, fmt.Errorf("state file exceeds %d bytes", maxStateBytes)
	}
	var value State
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return State{}, fmt.Errorf("parse state: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return State{}, fmt.Errorf("parse state: trailing JSON data")
	}
	if value.Version != 1 {
		return State{}, fmt.Errorf("unsupported state version %d", value.Version)
	}
	if value.Checks == nil {
		value.Checks = map[string]CheckState{}
	}
	if len(value.Checks) > 512 {
		return State{}, fmt.Errorf("state contains too many checks")
	}
	for id, check := range value.Checks {
		if id == "" || len(id) > 128 {
			return State{}, fmt.Errorf("state contains an invalid check ID")
		}
		if !validStatus(check.Current) || !validStatus(check.LastObserved) {
			return State{}, fmt.Errorf("state contains an invalid status for %q", id)
		}
		if check.ConsecutiveFailures < 0 || check.ConsecutiveSuccesses < 0 {
			return State{}, fmt.Errorf("state contains invalid counters for %q", id)
		}
		check.Title = textsafe.Sanitize(check.Title, 256)
		check.Message = textsafe.Sanitize(check.Message, 4096)
		if !check.Notified && !check.LastNotificationAt.IsZero() && check.Current != model.StatusOK {
			check.Notified = true
		}
		value.Checks[id] = check
	}
	return value, nil
}

func Save(path string, value State) error {
	value.Version = 1
	if value.Checks == nil {
		value.Checks = map[string]CheckState{}
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > maxStateBytes {
		return fmt.Errorf("state file would exceed %d bytes", maxStateBytes)
	}
	return securefile.Write(path, data, 0600)
}

func validStatus(status model.Status) bool {
	return status == model.StatusOK || status == model.StatusWarning || status == model.StatusCritical
}

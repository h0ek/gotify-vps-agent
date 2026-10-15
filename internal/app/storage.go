package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"syscall"
	"time"

	"github.com/h0ek/gotify-vps-agent/internal/config"
	"github.com/h0ek/gotify-vps-agent/internal/gotify"
	"github.com/h0ek/gotify-vps-agent/internal/model"
	"github.com/h0ek/gotify-vps-agent/internal/notify"
	"github.com/h0ek/gotify-vps-agent/internal/queue"
	"github.com/h0ek/gotify-vps-agent/internal/state"
)

type persistenceError struct {
	Stage string
	Err   error
}

func (err *persistenceError) Error() string {
	return fmt.Sprintf("persist %s: %v", err.Stage, err.Err)
}

func (err *persistenceError) Unwrap() error {
	return err.Err
}

type persistence struct {
	Queue  func(queue.Queue) error
	State  func(state.State) error
	Cursor func(string) error
}

func restorePendingState(current *state.State, pending queue.Queue) {
	if current.Checks == nil {
		current.Checks = make(map[string]state.CheckState)
	}
	latest := make(map[string]queue.Event)
	for _, event := range pending.Events {
		latest[event.CheckID] = event
	}
	for id, event := range latest {
		if event.Kind == model.EventOccurrence {
			continue
		}
		check := current.Checks[id]
		if event.Kind == model.EventRecovery {
			if check.Notified {
				check.Current, check.LastObserved = model.StatusOK, model.StatusOK
				check.Notified = false
				check.ConsecutiveFailures = 0
				check.ConsecutiveSuccesses = max(check.ConsecutiveSuccesses, 1)
				current.Checks[id] = check
			}
			continue
		}
		if event.Kind != model.EventProblem && event.Kind != model.EventEscalation && event.Kind != model.EventReminder {
			continue
		}
		if !check.LastRecoveryAt.IsZero() && !check.LastRecoveryAt.Before(event.CreatedAt) {
			continue
		}
		if check.Notified || (event.Status != model.StatusWarning && event.Status != model.StatusCritical) {
			continue
		}
		check.Title, check.Message = event.Title, event.Message
		check.Current, check.LastObserved = event.Status, event.Status
		check.Notified = true
		check.LastNotificationAt = event.CreatedAt
		check.FirstFailureAt = event.CreatedAt
		check.ConsecutiveFailures = max(check.ConsecutiveFailures, 1)
		check.ConsecutiveSuccesses = 0
		current.Checks[id] = check
	}
}

func persistCheck(current state.State, pending queue.Queue, cursor string, saves persistence) error {
	if err := saves.Queue(pending); err != nil {
		return &persistenceError{Stage: "notification queue", Err: err}
	}
	if err := saves.State(current); err != nil {
		return &persistenceError{Stage: "check state", Err: err}
	}
	if err := saves.Cursor(cursor); err != nil {
		return &persistenceError{Stage: "journal cursor", Err: err}
	}
	return nil
}

func storageUnavailable(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EROFS) || errors.Is(err, syscall.EDQUOT) || errors.Is(err, syscall.EIO)
}

func reportStorageFailure(ctx context.Context, cfg config.Config, results []model.Result, failure *persistenceError, output io.Writer) error {
	emergencyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	send := func(ctx context.Context, title, body string, priority int) error {
		token, err := config.Token(cfg.Gotify.TokenFile)
		if err != nil {
			return err
		}
		timeout, err := time.ParseDuration(cfg.Gotify.Timeout)
		if err != nil {
			return err
		}
		client, err := gotify.New(cfg.Gotify.URL, token, min(timeout, 10*time.Second), cfg.Gotify.AllowInsecureHTTP, cfg.Gotify.ProxyURL)
		if err != nil {
			return err
		}
		return client.Send(ctx, title, body, priority)
	}
	return sendStorageFailure(emergencyCtx, cfg.Agent.Hostname, results, failure, send, output)
}

func sendStorageFailure(ctx context.Context, hostname string, results []model.Result, failure *persistenceError, send func(context.Context, string, string, int) error, output io.Writer) error {
	message := notify.StorageFailure(hostname, failure.Stage, failure.Err, results, time.Now().UTC())
	if err := send(ctx, message.Title, message.Body, message.Priority); err != nil {
		return fmt.Errorf("%w; emergency notification failed: %v", failure, err)
	}
	fmt.Fprintln(output, "Emergency notification delivered; persistent delivery state was not updated and the alert may be repeated.")
	return fmt.Errorf("%w; emergency notification delivered without updating persistent delivery state", failure)
}

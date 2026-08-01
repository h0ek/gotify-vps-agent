package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

type Status string

const (
	StatusOK       Status = "OK"
	StatusWarning  Status = "WARNING"
	StatusCritical Status = "CRITICAL"
)

type Result struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    Status `json:"status"`
	Message   string `json:"message"`
	Immediate bool   `json:"immediate,omitempty"`
}

type EventKind string

const (
	EventProblem    EventKind = "problem"
	EventEscalation EventKind = "escalation"
	EventRecovery   EventKind = "recovery"
	EventReminder   EventKind = "reminder"
)

type Event struct {
	CheckID     string    `json:"check_id"`
	Title       string    `json:"title"`
	Status      Status    `json:"status"`
	Kind        EventKind `json:"kind"`
	Message     string    `json:"message"`
	Priority    int       `json:"priority"`
	Fingerprint string    `json:"fingerprint"`
}

func (r Result) Fingerprint() string {
	value := fmt.Sprintf("%s\x00%s\x00%s", r.ID, r.Status, strings.TrimSpace(r.Message))
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func Priority(status Status, kind EventKind) int {
	if kind == EventRecovery {
		return 3
	}
	switch status {
	case StatusCritical:
		return 10
	case StatusWarning:
		return 5
	default:
		return 3
	}
}

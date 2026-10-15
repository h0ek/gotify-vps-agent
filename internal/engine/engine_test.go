package engine

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/h0ek/gotify-vps-agent/internal/config"
	"github.com/h0ek/gotify-vps-agent/internal/model"
	"github.com/h0ek/gotify-vps-agent/internal/state"
)

func TestWarningAndRecoveryThresholds(t *testing.T) {
	cfg := config.Default()
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	value := state.New()
	problem := model.Result{ID: "disk", Title: "Disk", Status: model.StatusWarning, Message: "usage 90%"}
	ok := model.Result{ID: "disk", Title: "Disk", Status: model.StatusOK, Message: "usage 50%"}

	if events := Evaluate(&value, []model.Result{problem}, cfg, now); len(events) != 0 {
		t.Fatalf("first warning should be suppressed")
	}
	if events := Evaluate(&value, []model.Result{problem}, cfg, now.Add(5*time.Minute)); len(events) != 1 || events[0].Kind != model.EventProblem {
		t.Fatalf("second warning should notify: %#v", events)
	}
	if events := Evaluate(&value, []model.Result{ok}, cfg, now.Add(10*time.Minute)); len(events) != 0 {
		t.Fatalf("first success should be suppressed")
	}
	if events := Evaluate(&value, []model.Result{ok}, cfg, now.Add(15*time.Minute)); len(events) != 1 || events[0].Kind != model.EventRecovery {
		t.Fatalf("second success should recover: %#v", events)
	}
}

func TestImmediateCritical(t *testing.T) {
	cfg := config.Default()
	value := state.New()
	result := model.Result{ID: "oom", Title: "OOM", Status: model.StatusCritical, Message: "killed process", Immediate: true}
	events := Evaluate(&value, []model.Result{result}, cfg, time.Now())
	if len(events) != 1 || events[0].Status != model.StatusCritical {
		t.Fatalf("expected immediate critical event: %#v", events)
	}
}

func TestBaselineProblemDoesNotCreateFalseRecovery(t *testing.T) {
	cfg := config.Default()
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	value := state.New()
	Baseline(&value, []model.Result{{ID: "timer", Title: "Timer", Status: model.StatusWarning, Message: "disabled"}}, now)
	okResult := model.Result{ID: "timer", Title: "Timer", Status: model.StatusOK, Message: "enabled"}
	if events := Evaluate(&value, []model.Result{okResult}, cfg, now.Add(5*time.Minute)); len(events) != 0 {
		t.Fatalf("unexpected first recovery event: %#v", events)
	}
	if events := Evaluate(&value, []model.Result{okResult}, cfg, now.Add(10*time.Minute)); len(events) != 0 {
		t.Fatalf("baseline-only problem must not recover: %#v", events)
	}
	if value.Checks["timer"].Current != model.StatusOK {
		t.Fatal("check did not return to OK")
	}
}

func TestBaselinePersistentProblemNotifies(t *testing.T) {
	cfg := config.Default()
	now := time.Now().UTC()
	for _, status := range []model.Status{model.StatusWarning, model.StatusCritical} {
		t.Run(string(status), func(t *testing.T) {
			value := state.New()
			result := model.Result{ID: "nginx", Title: "Nginx", Status: status, Message: "inactive", Immediate: status == model.StatusCritical}
			Baseline(&value, []model.Result{result}, now)
			events := Evaluate(&value, []model.Result{result}, cfg, now.Add(5*time.Minute))
			if len(events) != 1 || events[0].Kind != model.EventProblem {
				t.Fatalf("persistent baseline failure was suppressed: %#v", events)
			}
		})
	}
}

func TestMixedFailureSeveritiesStillNotify(t *testing.T) {
	cfg := config.Default()
	now := time.Now().UTC()
	for _, statuses := range [][]model.Status{{model.StatusWarning, model.StatusCritical}, {model.StatusCritical, model.StatusWarning}} {
		value := state.New()
		for index, status := range statuses {
			events := Evaluate(&value, []model.Result{{ID: "memory", Status: status, Message: "low memory"}}, cfg, now.Add(time.Duration(index)*5*time.Minute))
			if index == 1 && (len(events) != 1 || events[0].Kind != model.EventProblem) {
				t.Fatalf("continuous failure did not notify for %v: %#v", statuses, events)
			}
		}
		if !value.Checks["memory"].FirstFailureAt.Equal(now) {
			t.Fatal("severity change reset the incident start")
		}
	}
}

func TestInactiveChecksAreRemoved(t *testing.T) {
	cfg := config.Default()
	value := state.New()
	now := time.Now().UTC()
	Evaluate(&value, []model.Result{{ID: "service.nginx", Status: model.StatusCritical, Immediate: true}}, cfg, now)
	if events := Evaluate(&value, nil, cfg, now.Add(5*time.Minute)); len(events) != 0 || len(value.Checks) != 0 {
		t.Fatalf("disabled check remained active: %#v, %#v", events, value.Checks)
	}
}

func TestNotifiedSeverityChangesStillRequireConfirmation(t *testing.T) {
	cfg := config.Default()
	cfg.Agent.CriticalReminderHours = 6
	now := time.Now().UTC()
	value := state.New()
	critical := model.Result{ID: "memory", Status: model.StatusCritical, Message: "low memory"}
	warning := model.Result{ID: "memory", Status: model.StatusWarning, Message: "low memory"}
	Evaluate(&value, []model.Result{warning}, cfg, now)
	Evaluate(&value, []model.Result{warning}, cfg, now.Add(5*time.Minute))
	if events := Evaluate(&value, []model.Result{critical}, cfg, now.Add(10*time.Minute)); len(events) != 0 {
		t.Fatalf("one critical sample caused an unconfirmed escalation: %#v", events)
	}
	if events := Evaluate(&value, []model.Result{critical}, cfg, now.Add(15*time.Minute)); len(events) != 1 || events[0].Kind != model.EventEscalation {
		t.Fatalf("persistent critical condition did not escalate: %#v", events)
	}
	for index, result := range []model.Result{warning, critical, warning, critical} {
		if events := Evaluate(&value, []model.Result{result}, cfg, now.Add(time.Duration(index+4)*5*time.Minute)); len(events) != 0 {
			t.Fatalf("severity flapping caused duplicate alerts: %#v", events)
		}
	}
	if events := Evaluate(&value, []model.Result{critical}, cfg, now.Add(7*time.Hour)); len(events) != 1 || events[0].Kind != model.EventReminder {
		t.Fatalf("persistent failure did not remind: %#v", events)
	}
}

func TestQuietIncidentAcrossRestartsAndSeverityChanges(t *testing.T) {
	for _, notifyRecovery := range []bool{true, false} {
		t.Run(fmt.Sprintf("recovery=%t", notifyRecovery), func(t *testing.T) {
			cfg := config.Default()
			cfg.Agent.NotifyRecovery = notifyRecovery
			value := state.New()
			path := filepath.Join(t.TempDir(), "state.json")
			now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
			recovery := model.EventKind("")
			if notifyRecovery {
				recovery = model.EventRecovery
			}
			steps := []struct {
				status model.Status
				kind   model.EventKind
			}{
				{model.StatusWarning, ""},
				{model.StatusWarning, model.EventProblem},
				{model.StatusCritical, ""},
				{model.StatusCritical, model.EventEscalation},
				{model.StatusWarning, ""},
				{model.StatusWarning, ""},
				{model.StatusCritical, ""},
				{model.StatusCritical, ""},
				{model.StatusOK, ""},
				{model.StatusWarning, ""},
				{model.StatusWarning, ""},
				{model.StatusCritical, ""},
				{model.StatusCritical, ""},
				{model.StatusOK, ""},
				{model.StatusOK, recovery},
				{model.StatusOK, ""},
				{model.StatusWarning, ""},
				{model.StatusWarning, model.EventProblem},
				{model.StatusWarning, ""},
				{model.StatusWarning, ""},
			}
			for index, step := range steps {
				result := model.Result{ID: "memory", Status: step.status, Message: fmt.Sprintf("measurement %d", index)}
				events := Evaluate(&value, []model.Result{result}, cfg, now.Add(time.Duration(index)*48*time.Hour))
				if step.kind == "" {
					if len(events) != 0 {
						t.Fatalf("step %d repeated a notification: %#v", index, events)
					}
				} else if len(events) != 1 || events[0].Kind != step.kind {
					t.Fatalf("step %d: expected %s, got %#v", index, step.kind, events)
				}
				if index >= 3 && index < 14 && value.Checks["memory"].Current != model.StatusCritical {
					t.Fatalf("step %d forgot the incident's highest severity", index)
				}
				if index == 14 && (value.Checks["memory"].Current != model.StatusOK || value.Checks["memory"].Notified) {
					t.Fatal("confirmed recovery did not close the incident")
				}
				if err := state.Save(path, value); err != nil {
					t.Fatal(err)
				}
				var err error
				value, err = state.Load(path)
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestRemindersCanBeEnabledIndependently(t *testing.T) {
	for _, status := range []model.Status{model.StatusWarning, model.StatusCritical} {
		for _, enabled := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/enabled=%t", status, enabled), func(t *testing.T) {
				cfg := config.Default()
				cfg.Agent.WarningFailures = 1
				cfg.Agent.WarningReminderHours, cfg.Agent.CriticalReminderHours = 1, 1
				if !enabled {
					if status == model.StatusWarning {
						cfg.Agent.WarningReminderHours = 0
					} else {
						cfg.Agent.CriticalReminderHours = 0
					}
				}
				value := state.New()
				now := time.Now().UTC()
				result := model.Result{ID: "disk", Status: status, Message: "full"}
				if events := Evaluate(&value, []model.Result{result}, cfg, now); len(events) != 1 {
					t.Fatalf("initial problem was suppressed: %#v", events)
				}
				events := Evaluate(&value, []model.Result{result}, cfg, now.Add(2*time.Hour))
				if enabled {
					if len(events) != 1 || events[0].Kind != model.EventReminder {
						t.Fatalf("configured reminder was suppressed: %#v", events)
					}
				} else if len(events) != 0 {
					t.Fatalf("disabled reminder was sent: %#v", events)
				}
			})
		}
	}
}

func TestIndependentJournalOccurrencesAndReplay(t *testing.T) {
	for _, id := range []string{"memory.oom", "kernel.errors"} {
		value := state.New()
		cfg := config.Default()
		now := time.Now().UTC()
		result := model.Result{ID: id, Status: model.StatusCritical, Message: "identical kernel message", Immediate: true, Occurrence: "first-cursor"}
		first := Evaluate(&value, []model.Result{result}, cfg, now)
		result.Occurrence = "second-cursor"
		second := Evaluate(&value, []model.Result{result}, cfg, now.Add(5*time.Minute))
		if len(first) != 1 || len(second) != 1 || first[0].Kind != model.EventOccurrence || first[0].Fingerprint == second[0].Fingerprint {
			t.Fatalf("distinct events merged: first=%#v second=%#v", first, second)
		}
		if events := Evaluate(&value, []model.Result{result}, cfg, now.Add(10*time.Minute)); len(events) != 0 {
			t.Fatalf("same event replayed: %#v", events)
		}
		if events := Evaluate(&value, []model.Result{{ID: id, Status: model.StatusOK, Message: "No new matching events"}}, cfg, now.Add(15*time.Minute)); len(events) != 0 {
			t.Fatalf("occurrence generated false recovery: %#v", events)
		}
		if events := Evaluate(&value, []model.Result{result}, cfg, now.Add(20*time.Minute)); len(events) != 0 {
			t.Fatalf("event replayed after successful read: %#v", events)
		}
	}
}

package engine

import (
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

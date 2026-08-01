package app

import (
	"path/filepath"
	"testing"
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

func TestParseProperties(t *testing.T) {
	values := parseProperties("LoadState=loaded\nActiveState=active\nEmpty=\n")
	if values["LoadState"] != "loaded" || values["ActiveState"] != "active" {
		t.Fatalf("unexpected properties: %#v", values)
	}
	if timerValueScheduled(values["Empty"]) {
		t.Fatal("empty timer value must not be scheduled")
	}
	if !timerValueScheduled("123456") {
		t.Fatal("non-zero timer value must be scheduled")
	}
}

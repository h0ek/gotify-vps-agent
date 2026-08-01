package runner

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunCapturesExitCode(t *testing.T) {
	result, err := Run(context.Background(), time.Second, "/usr/bin/false")
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 1 {
		t.Fatalf("unexpected exit code %d", result.ExitCode)
	}
}

func TestRunTimesOut(t *testing.T) {
	_, err := Run(context.Background(), 20*time.Millisecond, "/usr/bin/sleep", "1")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func TestLimitedBufferBoundsConcurrentOutput(t *testing.T) {
	buffer := &limitedBuffer{remaining: 8}
	done := make(chan struct{}, 2)
	for _, value := range []string{"abcdefgh", "ijklmnop"} {
		go func(value string) {
			_, _ = buffer.Write([]byte(value))
			done <- struct{}{}
		}(value)
	}
	<-done
	<-done
	text, truncated := buffer.result()
	if len(text) > 8 || !truncated {
		t.Fatalf("unexpected buffer result %q truncated=%t", text, truncated)
	}
}

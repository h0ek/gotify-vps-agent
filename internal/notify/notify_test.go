package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/h0ek/gotify-vps-agent/internal/model"
	"github.com/h0ek/gotify-vps-agent/internal/queue"
)

func TestBuildEscapesEventMarkdown(t *testing.T) {
	message := Build("host", []queue.Event{{
		Event: model.Event{Title: "Disk [root]", Message: "value *critical*\n<script>alert(1)</script>", Status: model.StatusCritical, Kind: model.EventProblem, Priority: 10},
	}}, time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC))
	if !strings.Contains(message.Body, `Disk \[root\]`) || !strings.Contains(message.Body, `value \*critical\* &lt;script&gt;alert(1)&lt;/script&gt;`) {
		t.Fatalf("message was not escaped: %s", message.Body)
	}
	if message.Priority != 10 {
		t.Fatalf("unexpected priority: %d", message.Priority)
	}
}

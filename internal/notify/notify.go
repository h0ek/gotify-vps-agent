package notify

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/h0ek/gotify-vps-agent/internal/gotify"
	"github.com/h0ek/gotify-vps-agent/internal/model"
	"github.com/h0ek/gotify-vps-agent/internal/queue"
	"github.com/h0ek/gotify-vps-agent/internal/textsafe"
)

type Message struct {
	Title    string
	Body     string
	Priority int
}

func StorageFailure(hostname, stage string, err error, results []model.Result, now time.Time) Message {
	events := []queue.Event{{Event: model.Event{CheckID: "agent.storage", Title: "Agent persistence failure", Status: model.StatusCritical, Kind: model.EventProblem, Priority: 10, Message: "Unable to persist " + stage + ": " + textsafe.Sanitize(err.Error(), 512) + ". This emergency alert was not recorded and may be repeated."}, CreatedAt: now}}
	for _, status := range []model.Status{model.StatusCritical, model.StatusWarning} {
		for _, result := range results {
			if result.Status != status || len(events) >= 8 {
				continue
			}
			events = append(events, queue.Event{Event: model.Event{CheckID: result.ID, Title: textsafe.Sanitize(result.Title, 256), Status: result.Status, Kind: model.EventProblem, Priority: model.Priority(result.Status, model.EventProblem), Message: textsafe.Sanitize(result.Message, 512)}, CreatedAt: now})
		}
	}
	message := Build(hostname, events, now)
	message.Title = "[" + hostname + "] Agent persistence failure"
	return message
}

func BuildBatch(hostname string, events []queue.Event, now time.Time) (Message, int, error) {
	var message Message
	count := 0
	for index := range events {
		next := Build(hostname, events[:index+1], now)
		if len(next.Body) > gotify.MaxMessageBytes {
			if count == 0 {
				return Message{}, 0, fmt.Errorf("notification for %q exceeds the message limit", events[index].CheckID)
			}
			break
		}
		message = next
		count = index + 1
	}
	return message, count, nil
}

func escapeMarkdown(value string) string {
	value = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(value)
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\\", "\\\\",
		"`", "\\`",
		"*", "\\*",
		"_", "\\_",
		"[", "\\[",
		"]", "\\]",
	)
	return replacer.Replace(value)
}

func Build(hostname string, events []queue.Event, now time.Time) Message {
	priority := 3
	problemCount := 0
	recoveryCount := 0
	for _, event := range events {
		if event.Priority > priority {
			priority = event.Priority
		}
		if event.Kind == model.EventRecovery {
			recoveryCount++
		} else {
			problemCount++
		}
	}

	title := fmt.Sprintf("[%s] %d VPS events", hostname, len(events))
	if problemCount > 0 && recoveryCount == 0 {
		word := "problems"
		if problemCount == 1 {
			word = "problem"
		}
		title = fmt.Sprintf("[%s] %d %s", hostname, problemCount, word)
	} else if recoveryCount > 0 && problemCount == 0 {
		word := "recoveries"
		if recoveryCount == 1 {
			word = "recovery"
		}
		title = fmt.Sprintf("[%s] %d %s", hostname, recoveryCount, word)
	}

	sections := []struct {
		name  string
		match func(queue.Event) bool
	}{
		{"CRITICAL", func(event queue.Event) bool {
			return event.Status == model.StatusCritical && event.Kind != model.EventRecovery
		}},
		{"WARNING", func(event queue.Event) bool {
			return event.Status == model.StatusWarning && event.Kind != model.EventRecovery
		}},
		{"RECOVERY", func(event queue.Event) bool { return event.Kind == model.EventRecovery }},
	}

	var body strings.Builder
	for _, section := range sections {
		items := make([]queue.Event, 0)
		for _, event := range events {
			if section.match(event) {
				items = append(items, event)
			}
		}
		if len(items) == 0 {
			continue
		}
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].Title == items[j].Title {
				return items[i].CreatedAt.Before(items[j].CreatedAt)
			}
			return items[i].Title < items[j].Title
		})
		fmt.Fprintf(&body, "## %s\n", section.name)
		for _, event := range items {
			marker := ""
			if event.Kind == model.EventReminder {
				marker = " (reminder)"
			} else if event.Kind == model.EventEscalation {
				marker = " (escalated)"
			} else if event.Kind == model.EventOccurrence {
				marker = " (new event)"
			}
			observed := ""
			if !event.CreatedAt.IsZero() {
				observed = " [" + event.CreatedAt.UTC().Format(time.RFC3339) + "]"
			}
			fmt.Fprintf(&body, "- **%s%s**%s — %s\n", escapeMarkdown(event.Title), marker, observed, escapeMarkdown(event.Message))
		}
		body.WriteString("\n")
	}
	fmt.Fprintf(&body, "Checked: `%s`", now.UTC().Format(time.RFC3339))
	return Message{Title: title, Body: body.String(), Priority: priority}
}

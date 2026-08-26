package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/h0ek/gotify-vps-agent/internal/checks"
	"github.com/h0ek/gotify-vps-agent/internal/config"
	"github.com/h0ek/gotify-vps-agent/internal/engine"
	"github.com/h0ek/gotify-vps-agent/internal/gotify"
	"github.com/h0ek/gotify-vps-agent/internal/model"
	"github.com/h0ek/gotify-vps-agent/internal/notify"
	"github.com/h0ek/gotify-vps-agent/internal/platform"
	"github.com/h0ek/gotify-vps-agent/internal/queue"
	"github.com/h0ek/gotify-vps-agent/internal/runner"
	"github.com/h0ek/gotify-vps-agent/internal/securefile"
	"github.com/h0ek/gotify-vps-agent/internal/state"
)

type CheckOptions struct {
	DryRun   bool
	Baseline bool
}

type lock struct {
	file *os.File
}

func acquireLock(path string) (*lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("another agent process is already running")
	}
	return &lock{file: file}, nil
}

func (value *lock) Close() {
	if value == nil || value.file == nil {
		return
	}
	_ = syscall.Flock(int(value.file.Fd()), syscall.LOCK_UN)
	_ = value.file.Close()
}

func RunCheck(ctx context.Context, configPath string, options CheckOptions, output io.Writer) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	if err := platform.RequireDebian13(); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("check must run as root")
	}

	processLock, err := acquireLock(config.LockPath(cfg))
	if err != nil {
		return err
	}
	defer processLock.Close()

	current, err := state.Load(config.StatePath(cfg))
	if err != nil {
		return err
	}
	pending, err := queue.Load(config.QueuePath(cfg))
	if err != nil {
		return err
	}
	runOutput := checks.Run(ctx, cfg, current.LastRun)
	results := runOutput.Results
	if cfg.Checks.DeliveryQueue {
		oldest := time.Time{}
		for _, event := range pending.Events {
			if oldest.IsZero() || event.CreatedAt.Before(oldest) {
				oldest = event.CreatedAt
			}
		}
		results = append(results, checks.DeliveryQueue(len(pending.Events), pending.Attempts, oldest))
	}
	printResults(output, results)
	if options.DryRun {
		fmt.Fprintln(output, "Dry run: state and notifications were not changed.")
		return nil
	}
	if err := os.MkdirAll(cfg.Agent.StateDir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(cfg.Agent.StateDir, 0700); err != nil {
		return err
	}

	now := time.Now().UTC()
	if options.Baseline {
		engine.Baseline(&current, results, now)
		if err := state.Save(config.StatePath(cfg), current); err != nil {
			return err
		}
		if err := writeJournalCursor(cfg, runOutput.JournalCursor); err != nil {
			return err
		}
		fmt.Fprintln(output, "Initial baseline saved. No health notification was sent.")
		return nil
	}

	events := engine.Evaluate(&current, results, cfg, now)
	pending.Add(events, now)
	if err := queue.Save(config.QueuePath(cfg), pending); err != nil {
		return err
	}
	if err := state.Save(config.StatePath(cfg), current); err != nil {
		return err
	}
	if err := writeJournalCursor(cfg, runOutput.JournalCursor); err != nil {
		return err
	}
	if len(pending.Events) == 0 {
		fmt.Fprintln(output, "No notification transitions or reminders.")
		return nil
	}
	if !pending.Ready(now) {
		fmt.Fprintf(output, "%d queued events; next Gotify retry at %s.\n", len(pending.Events), pending.NextAttemptAt.Format(time.RFC3339))
		return nil
	}

	token, err := config.Token(cfg.Gotify.TokenFile)
	if err != nil {
		return fmt.Errorf("read Gotify token: %w", err)
	}
	timeout, _ := time.ParseDuration(cfg.Gotify.Timeout)
	client, err := gotify.New(cfg.Gotify.URL, token, timeout, cfg.Gotify.AllowInsecureHTTP, cfg.Gotify.ProxyURL)
	if err != nil {
		return err
	}
	message := notify.Build(cfg.Agent.Hostname, pending.Events, now)
	if err := client.Send(ctx, message.Title, message.Body, message.Priority); err != nil {
		pending.MarkFailure(now)
		if saveErr := queue.Save(config.QueuePath(cfg), pending); saveErr != nil {
			return fmt.Errorf("Gotify delivery failed (%v) and retry state could not be saved: %w", err, saveErr)
		}
		fmt.Fprintf(output, "Gotify delivery failed; %d queued events retained, retry after %s: %v\n", len(pending.Events), pending.NextAttemptAt.Format(time.RFC3339), err)
		return nil
	}
	pending.Clear()
	if err := queue.Save(config.QueuePath(cfg), pending); err != nil {
		return err
	}
	fmt.Fprintln(output, "Gotify notification delivered.")
	return nil
}

func SendTest(ctx context.Context, configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	token, err := config.Token(cfg.Gotify.TokenFile)
	if err != nil {
		return err
	}
	timeout, _ := time.ParseDuration(cfg.Gotify.Timeout)
	client, err := gotify.New(cfg.Gotify.URL, token, timeout, cfg.Gotify.AllowInsecureHTTP, cfg.Gotify.ProxyURL)
	if err != nil {
		return err
	}
	return client.Send(ctx, "["+cfg.Agent.Hostname+"] Gotify VPS Agent test", "Gotify VPS Agent is configured and can send notifications.", 3)
}

func SendTestWithValues(ctx context.Context, cfg config.Config, token string) error {
	timeout, err := time.ParseDuration(cfg.Gotify.Timeout)
	if err != nil {
		return err
	}
	client, err := gotify.New(cfg.Gotify.URL, token, timeout, cfg.Gotify.AllowInsecureHTTP, cfg.Gotify.ProxyURL)
	if err != nil {
		return err
	}
	return client.Send(ctx, "["+cfg.Agent.Hostname+"] Gotify VPS Agent test", "Gotify VPS Agent configuration test succeeded.", 3)
}

func Status(configPath string, output io.Writer) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("status must run as root because runtime state is root-only")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	current, err := state.Load(config.StatePath(cfg))
	if err != nil {
		return err
	}
	pending, err := queue.Load(config.QueuePath(cfg))
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "Hostname: %s\n", cfg.Agent.Hostname)
	fmt.Fprintf(output, "Interval: %s\n", cfg.Agent.Interval)
	if cfg.Gotify.ProxyURL == "" {
		fmt.Fprintln(output, "SOCKS5 proxy: disabled")
	} else {
		fmt.Fprintf(output, "SOCKS5 proxy: %s\n", cfg.Gotify.ProxyURL)
	}
	if current.LastRun.IsZero() {
		fmt.Fprintln(output, "Last run: never")
	} else {
		fmt.Fprintf(output, "Last run: %s\n", current.LastRun.Format(time.RFC3339))
	}
	fmt.Fprintf(output, "Baseline initialized: %t\n", current.Initialized)
	fmt.Fprintf(output, "Queued events: %d\n", len(pending.Events))
	if pending.Attempts > 0 {
		fmt.Fprintf(output, "Delivery attempts: %d\n", pending.Attempts)
		fmt.Fprintf(output, "Next delivery attempt: %s\n", pending.NextAttemptAt.Format(time.RFC3339))
	}

	ids := make([]string, 0, len(current.Checks))
	for id := range current.Checks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		check := current.Checks[id]
		fmt.Fprintf(output, "%-10s %-28s %s\n", check.Current, id, check.Message)
	}
	return nil
}

func ResetState(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	processLock, err := acquireLock(config.LockPath(cfg))
	if err != nil {
		return err
	}
	defer processLock.Close()
	for _, path := range []string{config.StatePath(cfg), config.QueuePath(cfg), config.JournalCursorPath(cfg)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

type DoctorEntry struct {
	Status string
	Name   string
	Detail string
}

func Doctor(ctx context.Context, configPath string) ([]DoctorEntry, bool) {
	entries := make([]DoctorEntry, 0)
	failed := false
	add := func(status, name, detail string) {
		entries = append(entries, DoctorEntry{Status: status, Name: name, Detail: detail})
		if status == "FAIL" {
			failed = true
		}
	}
	if err := platform.RequireDebian13(); err != nil {
		add("FAIL", "Operating system", err.Error())
	} else {
		add("PASS", "Operating system", "Debian 13")
	}
	if os.Geteuid() != 0 {
		add("FAIL", "Privileges", "doctor must run as root")
	} else {
		add("PASS", "Privileges", "running as root")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		add("FAIL", "Configuration", err.Error())
		return entries, true
	}
	add("PASS", "Configuration", configPath)
	if cfg.Gotify.ProxyURL == "" {
		add("PASS", "SOCKS5 proxy", "disabled")
	} else {
		add("PASS", "SOCKS5 proxy", cfg.Gotify.ProxyURL)
	}
	checkFile(&entries, &failed, "/usr/local/bin/gotify-vps-agent", 0755)
	checkFile(&entries, &failed, "/usr/local/lib/gotify-vps-agent/uninstall.sh", 0750)
	checkFile(&entries, &failed, "/etc/systemd/system/gotify-vps-agent.service", 0644)
	checkFile(&entries, &failed, "/etc/systemd/system/gotify-vps-agent.timer", 0644)
	checkFile(&entries, &failed, configPath, 0644)
	checkFile(&entries, &failed, cfg.Gotify.TokenFile, 0600)
	if _, err := config.Token(cfg.Gotify.TokenFile); err != nil {
		add("FAIL", "Application token", err.Error())
	} else {
		add("PASS", "Application token", "present and not displayed")
	}
	if info, err := os.Lstat(cfg.Agent.StateDir); err != nil {
		add("FAIL", "State directory", err.Error())
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		add("FAIL", "State directory", cfg.Agent.StateDir+" must be a real directory")
	} else if info.Mode().Perm() != 0700 {
		add("FAIL", "State directory", fmt.Sprintf("mode is %04o, expected 0700", info.Mode().Perm()))
	} else if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != 0 || stat.Gid != 0 {
		add("FAIL", "State directory", cfg.Agent.StateDir+" must be root:root")
	} else {
		add("PASS", "State directory", cfg.Agent.StateDir+" 0700 root:root")
	}
	for _, path := range []string{config.StatePath(cfg), config.QueuePath(cfg), filepath.Join(cfg.Agent.StateDir, "install-manifest")} {
		if _, err := os.Stat(path); err == nil {
			checkFile(&entries, &failed, path, 0600)
		} else if !errors.Is(err, os.ErrNotExist) {
			add("FAIL", "Runtime file", path+": "+err.Error())
		}
	}
	for _, command := range []string{"/usr/bin/systemctl", "/usr/bin/journalctl", "/usr/bin/apt-get", "/usr/bin/dpkg", "/usr/bin/dpkg-query", "/usr/bin/timedatectl"} {
		if _, err := os.Stat(command); err != nil {
			add("FAIL", "Command", command+" not found")
		} else {
			add("PASS", "Command", command)
		}
	}
	if _, err := os.Stat("/usr/sbin/needrestart"); err != nil {
		add("WARN", "needrestart", "not installed; the kernel check will warn")
	} else {
		add("PASS", "needrestart", "/usr/sbin/needrestart")
	}
	if _, err := os.Stat(config.JournalCursorPath(cfg)); err == nil {
		checkFile(&entries, &failed, config.JournalCursorPath(cfg), 0600)
	} else if errors.Is(err, os.ErrNotExist) {
		add("WARN", "Journal cursor", "not initialized; create a baseline or run a check")
	} else {
		add("FAIL", "Journal cursor", err.Error())
	}
	timerResult, timerErr := runner.Run(ctx, 10*time.Second, "/usr/bin/systemctl", "show", "gotify-vps-agent.timer", "--property=LoadState,ActiveState,UnitFileState,NextElapseUSecRealtime,NextElapseUSecMonotonic")
	if timerErr != nil || timerResult.ExitCode != 0 {
		detail := "unable to inspect timer"
		if timerErr != nil {
			detail = timerErr.Error()
		} else if timerResult.Output != "" {
			detail = timerResult.Output
		}
		add("FAIL", "Agent timer", detail)
	} else {
		properties := parseProperties(timerResult.Output)
		scheduled := timerValueScheduled(properties["NextElapseUSecRealtime"]) || timerValueScheduled(properties["NextElapseUSecMonotonic"])
		if properties["LoadState"] == "loaded" && properties["ActiveState"] == "active" && properties["UnitFileState"] == "enabled" && scheduled {
			add("PASS", "Agent timer", "loaded, active, enabled and scheduled")
		} else {
			add("WARN", "Agent timer", strings.ReplaceAll(timerResult.Output, "\n", "; "))
		}
	}

	current, stateErr := state.Load(config.StatePath(cfg))
	if stateErr != nil {
		add("FAIL", "Runtime state", stateErr.Error())
	} else if current.LastRun.IsZero() {
		add("WARN", "Last completed run", "no completed run is recorded")
	} else {
		interval, _ := time.ParseDuration(cfg.Agent.Interval)
		age := time.Since(current.LastRun)
		if age > interval*3 {
			add("WARN", "Last completed run", fmt.Sprintf("%s ago", age.Round(time.Second)))
		} else {
			add("PASS", "Last completed run", fmt.Sprintf("%s ago", age.Round(time.Second)))
		}
	}

	pending, err := queue.Load(config.QueuePath(cfg))
	if err != nil {
		add("FAIL", "Notification queue", err.Error())
	} else if len(pending.Events) > 0 {
		detail := fmt.Sprintf("%d events waiting", len(pending.Events))
		if pending.Attempts > 0 {
			detail += fmt.Sprintf("; retry %d scheduled for %s", pending.Attempts+1, pending.NextAttemptAt.Format(time.RFC3339))
		}
		add("WARN", "Notification queue", detail)
	} else {
		add("PASS", "Notification queue", "empty")
	}
	token, tokenErr := config.Token(cfg.Gotify.TokenFile)
	timeout, timeoutErr := time.ParseDuration(cfg.Gotify.Timeout)
	if tokenErr == nil && timeoutErr == nil {
		client, clientErr := gotify.New(cfg.Gotify.URL, token, timeout, cfg.Gotify.AllowInsecureHTTP, cfg.Gotify.ProxyURL)
		if clientErr != nil {
			add("FAIL", "Gotify URL", clientErr.Error())
		} else if healthErr := client.Health(ctx); healthErr != nil {
			add("FAIL", "Gotify health", healthErr.Error())
		} else {
			add("PASS", "Gotify health", "server is reachable; no message was sent")
		}
	}
	return entries, failed
}

func parseProperties(output string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return values
}

func timerValueScheduled(value string) bool {
	return value != "" && value != "0" && value != "n/a"
}

func checkFile(entries *[]DoctorEntry, failed *bool, path string, expected os.FileMode) {
	add := func(status, name, detail string) {
		*entries = append(*entries, DoctorEntry{Status: status, Name: name, Detail: detail})
		if status == "FAIL" {
			*failed = true
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		add("FAIL", "File permissions", path+": "+err.Error())
		return
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		add("FAIL", "File type", path+" must be a regular file")
		return
	}
	if info.Mode().Perm() != expected {
		add("FAIL", "File permissions", fmt.Sprintf("%s mode is %04o, expected %04o", path, info.Mode().Perm(), expected))
		return
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || stat.Gid != 0 {
		add("FAIL", "File ownership", path+" must be root:root")
		return
	}
	add("PASS", "File permissions", fmt.Sprintf("%s %04o root:root", path, expected))
}

func writeJournalCursor(cfg config.Config, cursor string) error {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return nil
	}
	return securefile.Write(config.JournalCursorPath(cfg), []byte(cursor+"\n"), 0600)
}

func WriteConfig(path string, cfg config.Config) error {
	data, err := config.Marshal(cfg)
	if err != nil {
		return err
	}
	return securefile.Write(path, data, 0644)
}

func WriteToken(path, token string) error {
	return securefile.Write(path, []byte(strings.TrimSpace(token)+"\n"), 0600)
}

func printResults(output io.Writer, results []model.Result) {
	for _, result := range results {
		fmt.Fprintf(output, "%-8s %-30s %s\n", result.Status, result.Title, result.Message)
	}
}

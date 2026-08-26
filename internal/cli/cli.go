package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/h0ek/gotify-vps-agent/internal/app"
	"github.com/h0ek/gotify-vps-agent/internal/config"
	"github.com/h0ek/gotify-vps-agent/internal/platform"
	"github.com/h0ek/gotify-vps-agent/internal/services"
	"github.com/h0ek/gotify-vps-agent/internal/socksproxy"
	"github.com/h0ek/gotify-vps-agent/internal/systemd"
	"github.com/h0ek/gotify-vps-agent/internal/terminal"
	"github.com/h0ek/gotify-vps-agent/internal/version"
)

type CLI struct {
	In     io.Reader
	Out    io.Writer
	Err    io.Writer
	reader *bufio.Reader
}

func New(in io.Reader, out, errOut io.Writer) *CLI {
	return &CLI{In: in, Out: out, Err: errOut, reader: bufio.NewReader(in)}
}

func (c *CLI) Run(ctx context.Context, args []string) int {
	configPath := os.Getenv("GOTIFY_VPS_AGENT_CONFIG")
	if configPath == "" {
		configPath = config.DefaultConfigPath
	}
	global := flag.NewFlagSet("gotify-vps-agent", flag.ContinueOnError)
	global.SetOutput(c.Err)
	global.StringVar(&configPath, "config", configPath, "configuration file")
	if err := global.Parse(args); err != nil {
		return 2
	}
	remaining := global.Args()
	if len(remaining) == 0 {
		c.usage()
		return 2
	}

	var err error
	switch remaining[0] {
	case "configure":
		err = c.configure(ctx, configPath, remaining[1:])
	case "check":
		err = c.check(ctx, configPath, remaining[1:])
	case "status":
		err = app.Status(configPath, c.Out)
	case "doctor":
		err = c.doctor(ctx, configPath)
	case "test-notification":
		err = app.SendTest(ctx, configPath)
		if err == nil {
			fmt.Fprintln(c.Out, "Test notification delivered.")
		}
	case "services":
		err = c.services(ctx, configPath, remaining[1:])
	case "proxy":
		err = c.proxy(ctx, configPath, remaining[1:])
	case "timer":
		err = c.timer(ctx, configPath, remaining[1:])
	case "reset-state":
		err = c.resetState(configPath, remaining[1:])
	case "version":
		fmt.Fprintf(c.Out, "gotify-vps-agent %s commit=%s built=%s go=%s\n", version.Version, version.Commit, version.Date, runtime.Version())
	case "help", "-h", "--help":
		c.usage()
		return 0
	default:
		fmt.Fprintf(c.Err, "Unknown command %q\n", remaining[0])
		c.usage()
		return 2
	}
	if err != nil {
		fmt.Fprintln(c.Err, "Error:", err)
		return 1
	}
	return 0
}

func (c *CLI) check(ctx context.Context, configPath string, args []string) error {
	set := flag.NewFlagSet("check", flag.ContinueOnError)
	set.SetOutput(c.Err)
	dryRun := set.Bool("dry-run", false, "run checks without changing state or sending notifications")
	baseline := set.Bool("baseline", false, "save current results as the initial baseline")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *dryRun && *baseline {
		return fmt.Errorf("--dry-run and --baseline cannot be used together")
	}
	runContext, cancel := context.WithTimeout(ctx, 110*time.Second)
	defer cancel()
	return app.RunCheck(runContext, configPath, app.CheckOptions{DryRun: *dryRun, Baseline: *baseline}, c.Out)
}

func (c *CLI) configure(ctx context.Context, configPath string, args []string) error {
	set := flag.NewFlagSet("configure", flag.ContinueOnError)
	set.SetOutput(c.Err)
	allowHTTP := set.Bool("allow-insecure-http", false, "allow non-loopback plain HTTP")
	if err := set.Parse(args); err != nil {
		return err
	}
	if err := platform.RequireDebian13(); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("configure must run as root")
	}

	cfg := config.Default()
	existingToken := ""
	if loaded, err := config.Load(configPath); err == nil {
		cfg = loaded
		existingToken, _ = config.Token(cfg.Gotify.TokenFile)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	fmt.Fprintln(c.Out, "Gotify VPS Agent configuration")
	fmt.Fprintln(c.Out)
	cfg.Gotify.URL = c.promptString("Gotify server URL", cfg.Gotify.URL)
	if *allowHTTP {
		cfg.Gotify.AllowInsecureHTTP = true
	}
	if c.promptYesNo("Use a SOCKS5 proxy for Gotify", cfg.Gotify.ProxyURL != "") {
		proxyURL := cfg.Gotify.ProxyURL
		if proxyURL == "" {
			proxyURL = socksproxy.DefaultURL
		}
		cfg.Gotify.ProxyURL = c.promptString("SOCKS5 proxy URL", proxyURL)
	} else {
		cfg.Gotify.ProxyURL = ""
	}

	tokenPrompt := "Application token: "
	if existingToken != "" {
		tokenPrompt = "Application token [leave empty to keep current]: "
	}
	token, err := terminal.ReadPassword(tokenPrompt)
	if err != nil {
		return err
	}
	if token == "" {
		token = existingToken
	}
	if token == "" {
		return fmt.Errorf("application token is required")
	}

	cfg.Agent.Hostname = c.promptString("Notification hostname", cfg.Agent.Hostname)
	cfg.Agent.Interval = c.promptString("Check interval", cfg.Agent.Interval)
	cfg.Thresholds.DiskWarning = c.promptFloat("Disk warning threshold", cfg.Thresholds.DiskWarning)
	cfg.Thresholds.DiskCritical = c.promptFloat("Disk critical threshold", cfg.Thresholds.DiskCritical)

	detected := services.Detect(ctx)
	detectedByID := map[string]services.Detection{}
	for _, item := range detected {
		detectedByID[item.Profile.ID] = item
	}
	fmt.Fprintln(c.Out)
	fmt.Fprintln(c.Out, "Detected supported services:")
	for _, profile := range services.Profiles() {
		item, ok := detectedByID[profile.ID]
		if !ok {
			fmt.Fprintf(c.Out, "[ ] %-20s not detected\n", profile.Name)
			continue
		}
		fmt.Fprintf(c.Out, "[x] %-20s %s active=%t enabled=%t evidence=%s\n", profile.Name, item.Unit, item.Active, item.Enabled, strings.Join(item.Evidence, ","))
	}

	selected := map[string]string{}
	if len(detected) > 0 {
		if c.promptYesNo("Enable all detected service checks", true) {
			for _, item := range detected {
				selected[item.Profile.ID] = item.Unit
			}
		} else {
			for _, item := range detected {
				if c.promptYesNo("Enable "+item.Profile.Name, true) {
					selected[item.Profile.ID] = item.Unit
				}
			}
		}
	}
	cfg.Services = selected
	if err := cfg.Validate(); err != nil {
		return err
	}

	fmt.Fprintln(c.Out)
	if !c.promptYesNo("Testing will send one Gotify message. Continue", true) {
		return fmt.Errorf("configuration cancelled before the connection test")
	}
	if err := app.SendTestWithValues(ctx, cfg, token); err != nil {
		return fmt.Errorf("Gotify connection test failed: %w", err)
	}
	fmt.Fprintln(c.Out, "Connection successful.")

	if err := os.MkdirAll(cfg.Agent.StateDir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(cfg.Agent.StateDir, 0700); err != nil {
		return err
	}
	if err := app.WriteToken(cfg.Gotify.TokenFile, token); err != nil {
		return err
	}
	if err := app.WriteConfig(configPath, cfg); err != nil {
		return err
	}
	if err := systemd.WriteTimerOverride(ctx, cfg.Agent.Interval); err != nil {
		return err
	}

	enableTimer := c.promptYesNo("Enable systemd timer", true)
	if enableTimer {
		if err := systemd.EnableTimer(ctx); err != nil {
			return err
		}
	} else {
		_ = systemd.DisableTimer(ctx)
	}
	if c.promptYesNo("Create initial baseline", true) {
		if err := app.RunCheck(ctx, configPath, app.CheckOptions{Baseline: true}, c.Out); err != nil {
			return err
		}
	}
	fmt.Fprintln(c.Out, "Configuration complete.")
	return nil
}

func (c *CLI) doctor(ctx context.Context, configPath string) error {
	entries, failed := app.Doctor(ctx, configPath)
	for _, entry := range entries {
		fmt.Fprintf(c.Out, "%-4s %-22s %s\n", entry.Status, entry.Name, entry.Detail)
	}
	if failed {
		return fmt.Errorf("doctor found failures")
	}
	return nil
}

func (c *CLI) proxy(ctx context.Context, configPath string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: proxy status|enable|disable")
	}
	command := args[0]
	if command == "status" {
		if len(args) != 1 {
			return fmt.Errorf("usage: proxy status")
		}
		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		fmt.Fprintf(c.Out, "Gotify server: %s\n", cfg.Gotify.URL)
		if cfg.Gotify.ProxyURL == "" {
			fmt.Fprintln(c.Out, "SOCKS5 proxy: disabled")
		} else {
			fmt.Fprintf(c.Out, "SOCKS5 proxy: enabled via %s\n", cfg.Gotify.ProxyURL)
		}
		return nil
	}
	if command != "enable" && command != "disable" {
		return fmt.Errorf("unknown proxy command %q", command)
	}
	if err := platform.RequireDebian13(); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("proxy configuration must run as root")
	}
	set := flag.NewFlagSet("proxy "+command, flag.ContinueOnError)
	set.SetOutput(c.Err)
	serverURL := set.String("server", "", "Gotify server URL")
	proxyURL := set.String("proxy", "", "SOCKS5 proxy URL")
	if err := set.Parse(args[1:]); err != nil {
		return err
	}
	if len(set.Args()) != 0 {
		return fmt.Errorf("unexpected proxy arguments: %s", strings.Join(set.Args(), " "))
	}
	if command == "disable" && *proxyURL != "" {
		return fmt.Errorf("--proxy is valid only with proxy enable")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if *serverURL == "" {
		*serverURL = c.promptString("Gotify server URL", cfg.Gotify.URL)
	}
	cfg.Gotify.URL = strings.TrimSpace(*serverURL)
	if command == "enable" {
		if *proxyURL == "" {
			current := cfg.Gotify.ProxyURL
			if current == "" {
				current = socksproxy.DefaultURL
			}
			*proxyURL = c.promptString("SOCKS5 proxy URL", current)
		}
		cfg.Gotify.ProxyURL = strings.TrimSpace(*proxyURL)
	} else {
		cfg.Gotify.ProxyURL = ""
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	token, err := config.Token(cfg.Gotify.TokenFile)
	if err != nil {
		return fmt.Errorf("read Gotify token: %w", err)
	}
	fmt.Fprintln(c.Out, "Testing Gotify delivery before saving configuration.")
	if err := app.SendTestWithValues(ctx, cfg, token); err != nil {
		return fmt.Errorf("Gotify connection test failed; configuration was not changed: %w", err)
	}
	if err := app.WriteConfig(configPath, cfg); err != nil {
		return err
	}
	if command == "enable" {
		fmt.Fprintf(c.Out, "SOCKS5 proxy enabled via %s.\n", cfg.Gotify.ProxyURL)
	} else {
		fmt.Fprintln(c.Out, "SOCKS5 proxy disabled.")
	}
	return nil
}

func (c *CLI) services(ctx context.Context, configPath string, args []string) error {
	if err := platform.RequireDebian13(); err != nil {
		return err
	}
	command := "list"
	if len(args) > 0 {
		command = args[0]
	}
	switch command {
	case "detect":
		detected := services.Detect(ctx)
		detectedByID := map[string]services.Detection{}
		for _, item := range detected {
			detectedByID[item.Profile.ID] = item
		}
		for _, profile := range services.Profiles() {
			item, ok := detectedByID[profile.ID]
			if !ok {
				fmt.Fprintf(c.Out, "%-12s %-20s not detected\n", profile.ID, profile.Name)
				continue
			}
			fmt.Fprintf(c.Out, "%-12s %-20s unit=%s active=%t enabled=%t evidence=%s\n", profile.ID, profile.Name, item.Unit, item.Active, item.Enabled, strings.Join(item.Evidence, ","))
		}
		return nil
	case "list":
		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		keys := make([]string, 0, len(cfg.Services))
		for key := range cfg.Services {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) == 0 {
			fmt.Fprintln(c.Out, "No service checks are enabled.")
			return nil
		}
		for _, key := range keys {
			fmt.Fprintf(c.Out, "%-12s %s\n", key, cfg.Services[key])
		}
		return nil
	case "enable":
		if len(args) != 2 {
			return fmt.Errorf("usage: services enable <service-id>")
		}
		if os.Geteuid() != 0 {
			return fmt.Errorf("service configuration must run as root")
		}
		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		detection, err := services.DetectByID(ctx, args[1])
		if err != nil {
			return err
		}
		cfg.Services[detection.Profile.ID] = detection.Unit
		if err := app.WriteConfig(configPath, cfg); err != nil {
			return err
		}
		fmt.Fprintf(c.Out, "Enabled %s using %s.\n", detection.Profile.ID, detection.Unit)
		return nil
	case "disable":
		if len(args) != 2 {
			return fmt.Errorf("usage: services disable <service-id>")
		}
		if os.Geteuid() != 0 {
			return fmt.Errorf("service configuration must run as root")
		}
		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		if _, ok := cfg.Services[args[1]]; !ok {
			return fmt.Errorf("service check %q is not enabled", args[1])
		}
		delete(cfg.Services, args[1])
		if err := app.WriteConfig(configPath, cfg); err != nil {
			return err
		}
		fmt.Fprintf(c.Out, "Disabled %s.\n", args[1])
		return nil
	default:
		return fmt.Errorf("unknown services command %q", command)
	}
}

func (c *CLI) timer(ctx context.Context, configPath string, args []string) error {
	if err := platform.RequireDebian13(); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("timer configuration must run as root")
	}
	if len(args) != 1 || args[0] != "sync" {
		return fmt.Errorf("usage: timer sync")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if err := systemd.WriteTimerOverride(ctx, cfg.Agent.Interval); err != nil {
		return err
	}
	fmt.Fprintf(c.Out, "Timer interval synchronized to %s.\n", cfg.Agent.Interval)
	return nil
}

func (c *CLI) resetState(configPath string, args []string) error {
	set := flag.NewFlagSet("reset-state", flag.ContinueOnError)
	set.SetOutput(c.Err)
	yes := set.Bool("yes", false, "confirm state removal")
	if err := set.Parse(args); err != nil {
		return err
	}
	if !*yes {
		return fmt.Errorf("use --yes to confirm state, journal cursor and queue removal")
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("reset-state must run as root")
	}
	if err := app.ResetState(configPath); err != nil {
		return err
	}
	fmt.Fprintln(c.Out, "State, journal cursor and queued events removed.")
	return nil
}

func (c *CLI) promptString(label, current string) string {
	if current == "" {
		fmt.Fprintf(c.Out, "%s: ", label)
	} else {
		fmt.Fprintf(c.Out, "%s [%s]: ", label, current)
	}
	value, _ := c.reader.ReadString('\n')
	value = strings.TrimSpace(value)
	if value == "" {
		return current
	}
	return value
}

func (c *CLI) promptFloat(label string, current float64) float64 {
	for {
		fmt.Fprintf(c.Out, "%s [%s]: ", label, strconv.FormatFloat(current, 'f', -1, 64))
		value, _ := c.reader.ReadString('\n')
		value = strings.TrimSpace(value)
		if value == "" {
			return current
		}
		parsed, err := strconv.ParseFloat(value, 64)
		if err == nil {
			return parsed
		}
		fmt.Fprintln(c.Err, "Enter a valid number.")
	}
}

func (c *CLI) promptYesNo(label string, defaultYes bool) bool {
	suffix := "[Y/n]"
	if !defaultYes {
		suffix = "[y/N]"
	}
	for {
		fmt.Fprintf(c.Out, "%s %s: ", label, suffix)
		value, _ := c.reader.ReadString('\n')
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			return defaultYes
		}
		if value == "y" || value == "yes" {
			return true
		}
		if value == "n" || value == "no" {
			return false
		}
	}
}

func (c *CLI) usage() {
	fmt.Fprintln(c.Out, `Gotify VPS Agent

Usage:
  gotify-vps-agent [--config PATH] <command>

Commands:
  configure [--allow-insecure-http]
  check [--dry-run|--baseline]
  status
  doctor
  test-notification
  services [list|detect|enable ID|disable ID]
  proxy status
  proxy enable [--server URL] [--proxy URL]
  proxy disable [--server URL]
  timer sync
  reset-state --yes
  version`)
}

package config

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxConfigBytes    = 256 * 1024
	DefaultConfigPath = "/etc/gotify-vps-agent/config.toml"
	DefaultTokenPath  = "/etc/gotify-vps-agent/gotify.token"
	DefaultStateDir   = "/var/lib/gotify-vps-agent"
)

type Config struct {
	Version    int
	Agent      Agent
	Gotify     Gotify
	Thresholds Thresholds
	Checks     Checks
	Services   map[string]string
}

type Agent struct {
	Hostname              string
	Interval              string
	StateDir              string
	WarningFailures       int
	RecoverySuccesses     int
	WarningReminderHours  int
	CriticalReminderHours int
}

type Gotify struct {
	URL               string
	TokenFile         string
	Timeout           string
	AllowInsecureHTTP bool
}

type Thresholds struct {
	DiskWarning                  float64
	DiskCritical                 float64
	InodeWarning                 float64
	InodeCritical                float64
	MemoryAvailableWarning       float64
	MemoryAvailableCritical      float64
	SwapWarning                  float64
	SwapCritical                 float64
	LoadWarningPerCPU            float64
	LoadCriticalPerCPU           float64
	UnattendedUpgradeMaxAgeHours int
}

type Checks struct {
	SystemdFailed      bool
	Disk               bool
	Inode              bool
	FilesystemReadOnly bool
	Memory             bool
	Swap               bool
	OOM                bool
	Load               bool
	KernelErrors       bool
	APT                bool
	DPKG               bool
	APTTimers          bool
	UnattendedUpgrades bool
	RebootRequired     bool
	Needrestart        bool
	TimeSync           bool
	AgentTimer         bool
	AgentFreshness     bool
	DeliveryQueue      bool
}

func Default() Config {
	hostname, _ := os.Hostname()
	return Config{
		Version: 1,
		Agent: Agent{
			Hostname:              hostname,
			Interval:              "5m",
			StateDir:              DefaultStateDir,
			WarningFailures:       2,
			RecoverySuccesses:     2,
			WarningReminderHours:  24,
			CriticalReminderHours: 6,
		},
		Gotify: Gotify{
			TokenFile: DefaultTokenPath,
			Timeout:   "10s",
		},
		Thresholds: Thresholds{
			DiskWarning:                  85,
			DiskCritical:                 95,
			InodeWarning:                 85,
			InodeCritical:                95,
			MemoryAvailableWarning:       15,
			MemoryAvailableCritical:      8,
			SwapWarning:                  50,
			SwapCritical:                 80,
			LoadWarningPerCPU:            1.5,
			LoadCriticalPerCPU:           3,
			UnattendedUpgradeMaxAgeHours: 48,
		},
		Checks: Checks{
			SystemdFailed:      true,
			Disk:               true,
			Inode:              true,
			FilesystemReadOnly: true,
			Memory:             true,
			Swap:               true,
			OOM:                true,
			Load:               true,
			KernelErrors:       true,
			APT:                true,
			DPKG:               true,
			APTTimers:          true,
			UnattendedUpgrades: true,
			RebootRequired:     true,
			Needrestart:        true,
			TimeSync:           true,
			AgentTimer:         true,
			AgentFreshness:     true,
			DeliveryQueue:      true,
		},
		Services: map[string]string{},
	}
}

func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil {
		return Config{}, err
	}
	if len(data) > maxConfigBytes {
		return Config{}, fmt.Errorf("configuration exceeds %d bytes", maxConfigBytes)
	}
	cfg := Default()
	if err := Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func Unmarshal(data []byte, cfg *Config) error {
	if cfg.Services == nil {
		cfg.Services = map[string]string{}
	}
	section := ""
	seen := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(stripComment(scanner.Text()))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			switch section {
			case "agent", "gotify", "thresholds", "checks", "services":
			default:
				return fmt.Errorf("line %d: unknown section %q", lineNumber, section)
			}
			continue
		}
		key, raw, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("line %d: expected key = value", lineNumber)
		}
		key = strings.TrimSpace(key)
		raw = strings.TrimSpace(raw)
		identity := section + "\x00" + key
		if seen[identity] {
			return fmt.Errorf("line %d: duplicate key %q in section %q", lineNumber, key, section)
		}
		seen[identity] = true
		if err := assign(cfg, section, key, raw); err != nil {
			return fmt.Errorf("line %d: %w", lineNumber, err)
		}
	}
	return scanner.Err()
}

func stripComment(line string) string {
	quoted := false
	escaped := false
	for i, r := range line {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && quoted {
			escaped = true
			continue
		}
		if r == '"' {
			quoted = !quoted
			continue
		}
		if r == '#' && !quoted {
			return line[:i]
		}
	}
	return line
}

func assign(cfg *Config, section, key, raw string) error {
	stringValue := func() (string, error) {
		value, err := strconv.Unquote(raw)
		if err != nil {
			return "", fmt.Errorf("invalid quoted string for %s", key)
		}
		return value, nil
	}
	boolValue := func() (bool, error) {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return false, fmt.Errorf("invalid boolean for %s", key)
		}
		return value, nil
	}
	intValue := func() (int, error) {
		value, err := strconv.Atoi(raw)
		if err != nil {
			return 0, fmt.Errorf("invalid integer for %s", key)
		}
		return value, nil
	}
	floatValue := func() (float64, error) {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid number for %s", key)
		}
		return value, nil
	}

	if section == "" && key == "version" {
		value, err := intValue()
		cfg.Version = value
		return err
	}

	switch section {
	case "agent":
		switch key {
		case "hostname":
			value, err := stringValue()
			cfg.Agent.Hostname = value
			return err
		case "interval":
			value, err := stringValue()
			cfg.Agent.Interval = value
			return err
		case "state_dir":
			value, err := stringValue()
			cfg.Agent.StateDir = value
			return err
		case "warning_failures":
			value, err := intValue()
			cfg.Agent.WarningFailures = value
			return err
		case "recovery_successes":
			value, err := intValue()
			cfg.Agent.RecoverySuccesses = value
			return err
		case "warning_reminder_hours":
			value, err := intValue()
			cfg.Agent.WarningReminderHours = value
			return err
		case "critical_reminder_hours":
			value, err := intValue()
			cfg.Agent.CriticalReminderHours = value
			return err
		}
	case "gotify":
		switch key {
		case "url":
			value, err := stringValue()
			cfg.Gotify.URL = value
			return err
		case "token_file":
			value, err := stringValue()
			cfg.Gotify.TokenFile = value
			return err
		case "timeout":
			value, err := stringValue()
			cfg.Gotify.Timeout = value
			return err
		case "allow_insecure_http":
			value, err := boolValue()
			cfg.Gotify.AllowInsecureHTTP = value
			return err
		}
	case "thresholds":
		switch key {
		case "disk_warning":
			value, err := floatValue()
			cfg.Thresholds.DiskWarning = value
			return err
		case "disk_critical":
			value, err := floatValue()
			cfg.Thresholds.DiskCritical = value
			return err
		case "inode_warning":
			value, err := floatValue()
			cfg.Thresholds.InodeWarning = value
			return err
		case "inode_critical":
			value, err := floatValue()
			cfg.Thresholds.InodeCritical = value
			return err
		case "memory_available_warning":
			value, err := floatValue()
			cfg.Thresholds.MemoryAvailableWarning = value
			return err
		case "memory_available_critical":
			value, err := floatValue()
			cfg.Thresholds.MemoryAvailableCritical = value
			return err
		case "swap_warning":
			value, err := floatValue()
			cfg.Thresholds.SwapWarning = value
			return err
		case "swap_critical":
			value, err := floatValue()
			cfg.Thresholds.SwapCritical = value
			return err
		case "load_warning_per_cpu":
			value, err := floatValue()
			cfg.Thresholds.LoadWarningPerCPU = value
			return err
		case "load_critical_per_cpu":
			value, err := floatValue()
			cfg.Thresholds.LoadCriticalPerCPU = value
			return err
		case "unattended_upgrade_max_age_hours":
			value, err := intValue()
			cfg.Thresholds.UnattendedUpgradeMaxAgeHours = value
			return err
		}
	case "checks":
		value, err := boolValue()
		if err != nil {
			return err
		}
		switch key {
		case "systemd_failed":
			cfg.Checks.SystemdFailed = value
		case "disk":
			cfg.Checks.Disk = value
		case "inode":
			cfg.Checks.Inode = value
		case "filesystem_read_only":
			cfg.Checks.FilesystemReadOnly = value
		case "memory":
			cfg.Checks.Memory = value
		case "swap":
			cfg.Checks.Swap = value
		case "oom":
			cfg.Checks.OOM = value
		case "load":
			cfg.Checks.Load = value
		case "kernel_errors":
			cfg.Checks.KernelErrors = value
		case "apt":
			cfg.Checks.APT = value
		case "dpkg":
			cfg.Checks.DPKG = value
		case "apt_timers":
			cfg.Checks.APTTimers = value
		case "unattended_upgrades":
			cfg.Checks.UnattendedUpgrades = value
		case "reboot_required":
			cfg.Checks.RebootRequired = value
		case "needrestart":
			cfg.Checks.Needrestart = value
		case "time_sync":
			cfg.Checks.TimeSync = value
		case "agent_timer":
			cfg.Checks.AgentTimer = value
		case "agent_freshness":
			cfg.Checks.AgentFreshness = value
		case "delivery_queue":
			cfg.Checks.DeliveryQueue = value
		default:
			return fmt.Errorf("unknown key %q in section %q", key, section)
		}
		return nil
	case "services":
		value, err := stringValue()
		if err != nil {
			return err
		}
		if key == "" || value == "" {
			return fmt.Errorf("service ID and unit must not be empty")
		}
		cfg.Services[key] = value
		return nil
	}
	return fmt.Errorf("unknown key %q in section %q", key, section)
}

func (cfg Config) Validate() error {
	if cfg.Version != 1 {
		return fmt.Errorf("unsupported config version %d", cfg.Version)
	}
	hostnamePattern := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
	if !hostnamePattern.MatchString(cfg.Agent.Hostname) {
		return fmt.Errorf("agent.hostname must contain 1-63 letters, digits, dots, underscores or hyphens")
	}
	interval, err := time.ParseDuration(cfg.Agent.Interval)
	if err != nil || interval < time.Minute || interval > 24*time.Hour {
		return fmt.Errorf("agent.interval must be between 1m and 24h")
	}
	if cfg.Agent.StateDir != DefaultStateDir {
		return fmt.Errorf("agent.state_dir must be %s", DefaultStateDir)
	}
	if cfg.Gotify.TokenFile != DefaultTokenPath {
		return fmt.Errorf("gotify.token_file must be %s", DefaultTokenPath)
	}
	if cfg.Agent.WarningFailures < 1 || cfg.Agent.WarningFailures > 100 || cfg.Agent.RecoverySuccesses < 1 || cfg.Agent.RecoverySuccesses > 100 {
		return fmt.Errorf("failure and recovery counters must be between 1 and 100")
	}
	if cfg.Agent.WarningReminderHours < 1 || cfg.Agent.WarningReminderHours > 8760 || cfg.Agent.CriticalReminderHours < 1 || cfg.Agent.CriticalReminderHours > 8760 {
		return fmt.Errorf("reminder intervals must be between 1 and 8760 hours")
	}
	timeout, err := time.ParseDuration(cfg.Gotify.Timeout)
	if err != nil || timeout < time.Second || timeout > time.Minute {
		return fmt.Errorf("gotify.timeout must be between 1s and 1m")
	}
	if cfg.Gotify.URL == "" {
		return fmt.Errorf("gotify.url must not be empty")
	}
	pairs := [][3]float64{
		{cfg.Thresholds.DiskWarning, cfg.Thresholds.DiskCritical, 100},
		{cfg.Thresholds.InodeWarning, cfg.Thresholds.InodeCritical, 100},
		{cfg.Thresholds.SwapWarning, cfg.Thresholds.SwapCritical, 100},
	}
	for _, pair := range pairs {
		if !finite(pair[0]) || !finite(pair[1]) || pair[0] < 0 || pair[1] <= pair[0] || pair[1] > pair[2] {
			return fmt.Errorf("invalid percentage thresholds")
		}
	}
	if !finite(cfg.Thresholds.MemoryAvailableCritical) || !finite(cfg.Thresholds.MemoryAvailableWarning) || cfg.Thresholds.MemoryAvailableCritical < 0 || cfg.Thresholds.MemoryAvailableWarning <= cfg.Thresholds.MemoryAvailableCritical || cfg.Thresholds.MemoryAvailableWarning > 100 {
		return fmt.Errorf("invalid memory available thresholds")
	}
	if !finite(cfg.Thresholds.LoadWarningPerCPU) || !finite(cfg.Thresholds.LoadCriticalPerCPU) || cfg.Thresholds.LoadWarningPerCPU <= 0 || cfg.Thresholds.LoadCriticalPerCPU <= cfg.Thresholds.LoadWarningPerCPU || cfg.Thresholds.LoadCriticalPerCPU > 1000 {
		return fmt.Errorf("invalid load thresholds")
	}
	if cfg.Thresholds.UnattendedUpgradeMaxAgeHours < 1 || cfg.Thresholds.UnattendedUpgradeMaxAgeHours > 8760 {
		return fmt.Errorf("unattended upgrade maximum age must be between 1 and 8760 hours")
	}
	allowedServices := map[string]bool{
		"ssh": true, "nginx": true, "php-fpm": true, "mariadb": true, "postgresql": true, "tor": true,
	}
	unitPattern := regexp.MustCompile(`^[A-Za-z0-9_.@:-]+\.service$`)
	for id, unit := range cfg.Services {
		if !allowedServices[id] {
			return fmt.Errorf("unsupported service profile %q", id)
		}
		if strings.HasPrefix(unit, "-") || !unitPattern.MatchString(unit) {
			return fmt.Errorf("invalid systemd service unit %q", unit)
		}
	}
	return nil
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func Marshal(cfg Config) ([]byte, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	var b strings.Builder
	writeString := func(key, value string) { fmt.Fprintf(&b, "%s = %s\n", key, strconv.Quote(value)) }
	writeBool := func(key string, value bool) { fmt.Fprintf(&b, "%s = %t\n", key, value) }
	writeInt := func(key string, value int) { fmt.Fprintf(&b, "%s = %d\n", key, value) }
	writeFloat := func(key string, value float64) {
		fmt.Fprintf(&b, "%s = %s\n", key, strconv.FormatFloat(value, 'f', -1, 64))
	}

	writeInt("version", cfg.Version)
	b.WriteString("\n[agent]\n")
	writeString("hostname", cfg.Agent.Hostname)
	writeString("interval", cfg.Agent.Interval)
	writeString("state_dir", cfg.Agent.StateDir)
	writeInt("warning_failures", cfg.Agent.WarningFailures)
	writeInt("recovery_successes", cfg.Agent.RecoverySuccesses)
	writeInt("warning_reminder_hours", cfg.Agent.WarningReminderHours)
	writeInt("critical_reminder_hours", cfg.Agent.CriticalReminderHours)

	b.WriteString("\n[gotify]\n")
	writeString("url", cfg.Gotify.URL)
	writeString("token_file", cfg.Gotify.TokenFile)
	writeString("timeout", cfg.Gotify.Timeout)
	writeBool("allow_insecure_http", cfg.Gotify.AllowInsecureHTTP)

	b.WriteString("\n[thresholds]\n")
	writeFloat("disk_warning", cfg.Thresholds.DiskWarning)
	writeFloat("disk_critical", cfg.Thresholds.DiskCritical)
	writeFloat("inode_warning", cfg.Thresholds.InodeWarning)
	writeFloat("inode_critical", cfg.Thresholds.InodeCritical)
	writeFloat("memory_available_warning", cfg.Thresholds.MemoryAvailableWarning)
	writeFloat("memory_available_critical", cfg.Thresholds.MemoryAvailableCritical)
	writeFloat("swap_warning", cfg.Thresholds.SwapWarning)
	writeFloat("swap_critical", cfg.Thresholds.SwapCritical)
	writeFloat("load_warning_per_cpu", cfg.Thresholds.LoadWarningPerCPU)
	writeFloat("load_critical_per_cpu", cfg.Thresholds.LoadCriticalPerCPU)
	writeInt("unattended_upgrade_max_age_hours", cfg.Thresholds.UnattendedUpgradeMaxAgeHours)

	b.WriteString("\n[checks]\n")
	writeBool("systemd_failed", cfg.Checks.SystemdFailed)
	writeBool("disk", cfg.Checks.Disk)
	writeBool("inode", cfg.Checks.Inode)
	writeBool("filesystem_read_only", cfg.Checks.FilesystemReadOnly)
	writeBool("memory", cfg.Checks.Memory)
	writeBool("swap", cfg.Checks.Swap)
	writeBool("oom", cfg.Checks.OOM)
	writeBool("load", cfg.Checks.Load)
	writeBool("kernel_errors", cfg.Checks.KernelErrors)
	writeBool("apt", cfg.Checks.APT)
	writeBool("dpkg", cfg.Checks.DPKG)
	writeBool("apt_timers", cfg.Checks.APTTimers)
	writeBool("unattended_upgrades", cfg.Checks.UnattendedUpgrades)
	writeBool("reboot_required", cfg.Checks.RebootRequired)
	writeBool("needrestart", cfg.Checks.Needrestart)
	writeBool("time_sync", cfg.Checks.TimeSync)
	writeBool("agent_timer", cfg.Checks.AgentTimer)
	writeBool("agent_freshness", cfg.Checks.AgentFreshness)
	writeBool("delivery_queue", cfg.Checks.DeliveryQueue)

	b.WriteString("\n[services]\n")
	keys := make([]string, 0, len(cfg.Services))
	for key := range cfg.Services {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		writeString(key, cfg.Services[key])
	}
	return []byte(b.String()), nil
}

func Token(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("token file is empty")
	}
	if len(token) > 4096 {
		return "", fmt.Errorf("token is unexpectedly long")
	}
	return token, nil
}

func StatePath(cfg Config) string { return strings.TrimRight(cfg.Agent.StateDir, "/") + "/state.json" }
func QueuePath(cfg Config) string { return strings.TrimRight(cfg.Agent.StateDir, "/") + "/queue.json" }
func LockPath(cfg Config) string  { return strings.TrimRight(cfg.Agent.StateDir, "/") + "/agent.lock" }
func JournalCursorPath(cfg Config) string {
	return strings.TrimRight(cfg.Agent.StateDir, "/") + "/journal.cursor"
}

package checks

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/h0ek/gotify-vps-agent/internal/config"
	"github.com/h0ek/gotify-vps-agent/internal/model"
	"github.com/h0ek/gotify-vps-agent/internal/runner"
	"github.com/h0ek/gotify-vps-agent/internal/textsafe"
)

type mount struct {
	Path     string
	Type     string
	Source   string
	ReadOnly bool
}

type filesystemUsage struct {
	Mount     mount
	DiskUsed  float64
	InodeUsed float64
}

type RunOutput struct {
	Results       []model.Result
	JournalCursor string
}

type journalSnapshot struct {
	Output string
	Cursor string
	Err    error
}

func Run(ctx context.Context, cfg config.Config, lastRun time.Time) RunOutput {
	results := make([]model.Result, 0, 36)
	journal := journalSnapshot{}
	if cfg.Checks.OOM || cfg.Checks.KernelErrors {
		journal = collectKernelJournal(ctx, config.JournalCursorPath(cfg), lastRun)
	}
	if cfg.Checks.SystemdFailed {
		results = append(results, checkSystemdFailed(ctx))
	}
	if cfg.Checks.Disk || cfg.Checks.Inode || cfg.Checks.FilesystemReadOnly {
		mounts, mountErr := readMounts()
		filesystems := inspectFilesystems(mounts)
		if cfg.Checks.Disk {
			results = append(results, checkDisk(filesystems, cfg))
		}
		if cfg.Checks.Inode {
			results = append(results, checkInode(filesystems, cfg))
		}
		if cfg.Checks.FilesystemReadOnly {
			results = append(results, checkReadOnly(mounts, mountErr))
		}
	}
	if cfg.Checks.Memory || cfg.Checks.Swap {
		memory := readMemInfo()
		if cfg.Checks.Memory {
			results = append(results, checkMemory(memory, cfg))
		}
		if cfg.Checks.Swap {
			results = append(results, checkSwap(memory, cfg))
		}
	}
	if cfg.Checks.OOM {
		results = append(results, checkOOM(journal))
	}
	if cfg.Checks.Load {
		results = append(results, checkLoad(cfg))
	}
	if cfg.Checks.KernelErrors {
		results = append(results, checkKernelErrors(journal))
	}
	if cfg.Checks.APT {
		results = append(results, checkAPT(ctx))
	}
	if cfg.Checks.DPKG {
		results = append(results, checkDPKG(ctx))
	}
	if cfg.Checks.APTTimers {
		results = append(results, checkAPTTimers(ctx))
	}
	if cfg.Checks.UnattendedUpgrades {
		results = append(results, checkUnattendedUpgrades(ctx, cfg, lastRun))
	}
	if cfg.Checks.RebootRequired {
		results = append(results, checkRebootRequired())
	}
	if cfg.Checks.Needrestart {
		results = append(results, checkNeedrestart(ctx))
	}
	if cfg.Checks.TimeSync {
		results = append(results, checkTimeSync(ctx))
	}
	if cfg.Checks.AgentTimer {
		results = append(results, checkAgentTimer(ctx))
	}
	if cfg.Checks.AgentFreshness {
		results = append(results, checkAgentFreshness(cfg, lastRun))
	}
	results = append(results, checkServices(ctx, cfg.Services)...)
	return RunOutput{Results: results, JournalCursor: journal.Cursor}
}

func checkSystemdFailed(ctx context.Context) model.Result {
	result, err := runner.Run(ctx, 10*time.Second, "/usr/bin/systemctl", "--failed", "--no-legend", "--plain")
	if err != nil {
		return problem("systemd.failed", "Failed systemd units", model.StatusWarning, err.Error(), false)
	}
	lines := nonEmptyLines(result.Output)
	if len(lines) == 0 {
		return ok("systemd.failed", "Failed systemd units", "No failed systemd units")
	}
	if len(lines) > 8 {
		lines = append(lines[:8], fmt.Sprintf("and %d more", len(lines)-8))
	}
	return problem("systemd.failed", "Failed systemd units", model.StatusWarning, strings.Join(lines, "; "), false)
}

func inspectFilesystems(mounts []mount) []filesystemUsage {
	values := make([]filesystemUsage, 0, len(mounts))
	for _, item := range mounts {
		var stat syscall.Statfs_t
		if err := syscall.Statfs(item.Path, &stat); err != nil || stat.Blocks == 0 {
			continue
		}
		diskUsed := float64(stat.Blocks-stat.Bavail) / float64(stat.Blocks) * 100
		inodeUsed := 0.0
		if stat.Files > 0 {
			inodeUsed = float64(stat.Files-stat.Ffree) / float64(stat.Files) * 100
		}
		values = append(values, filesystemUsage{Mount: item, DiskUsed: diskUsed, InodeUsed: inodeUsed})
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Mount.Path < values[j].Mount.Path })
	return values
}

const hostMountInfoPath = "/proc/1/mountinfo"

func readMounts() ([]mount, error) {
	file, err := os.Open(hostMountInfoPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return parseMountInfo(file)
}

func parseMountInfo(reader io.Reader) ([]mount, error) {
	ignoredTypes := map[string]bool{
		"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true, "tmpfs": true,
		"cgroup": true, "cgroup2": true, "pstore": true, "securityfs": true,
		"debugfs": true, "tracefs": true, "configfs": true, "fusectl": true,
		"mqueue": true, "hugetlbfs": true, "autofs": true, "rpc_pipefs": true,
		"efivarfs": true, "binfmt_misc": true, "squashfs": true, "iso9660": true,
		"nfs": true, "nfs4": true, "cifs": true, "smb3": true, "9p": true,
		"ceph": true, "glusterfs": true, "fuse.sshfs": true,
	}
	seen := map[string]bool{}
	mounts := make([]mount, 0)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		separator := -1
		for i, field := range fields {
			if field == "-" {
				separator = i
				break
			}
		}
		if separator < 6 || len(fields) <= separator+2 {
			continue
		}
		path := unescapeMount(fields[4])
		options := strings.Split(fields[5], ",")
		fsType := fields[separator+1]
		source := unescapeMount(fields[separator+2])
		if ignoredTypes[fsType] || ignoredMountPath(path, fsType) || seen[path] {
			continue
		}
		readOnly := false
		for _, option := range options {
			if option == "ro" {
				readOnly = true
				break
			}
		}
		seen[path] = true
		mounts = append(mounts, mount{Path: path, Type: fsType, Source: source, ReadOnly: readOnly})
	}
	return mounts, scanner.Err()
}

func ignoredMountPath(path, fsType string) bool {
	if strings.HasPrefix(fsType, "fuse.") {
		return true
	}
	prefixes := []string{
		"/proc", "/sys", "/dev", "/run", "/var/lib/docker/overlay2",
		"/var/lib/containers/storage/overlay", "/var/lib/kubelet/pods",
	}
	for _, prefix := range prefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return fsType == "overlay" && path != "/"
}

func unescapeMount(value string) string {
	replacer := strings.NewReplacer("\\040", " ", "\\011", "\t", "\\012", "\n", "\\134", "\\")
	return replacer.Replace(value)
}

func checkDisk(values []filesystemUsage, cfg config.Config) model.Result {
	if len(values) == 0 {
		return problem("filesystem.disk", "Disk usage", model.StatusWarning, "No local filesystems could be inspected", false)
	}
	status := model.StatusOK
	issues := make([]string, 0)
	worst := values[0]
	for _, value := range values {
		if value.DiskUsed > worst.DiskUsed {
			worst = value
		}
		current := thresholdHigh(value.DiskUsed, cfg.Thresholds.DiskWarning, cfg.Thresholds.DiskCritical)
		status = higher(status, current)
		if current != model.StatusOK {
			issues = append(issues, fmt.Sprintf("%s %.1f%%", value.Mount.Path, value.DiskUsed))
		}
	}
	if status == model.StatusOK {
		return ok("filesystem.disk", "Disk usage", fmt.Sprintf("Highest usage is %.1f%% on %s", worst.DiskUsed, worst.Mount.Path))
	}
	return problem("filesystem.disk", "Disk usage", status, strings.Join(issues, "; "), status == model.StatusCritical)
}

func checkInode(values []filesystemUsage, cfg config.Config) model.Result {
	if len(values) == 0 {
		return problem("filesystem.inode", "Inode usage", model.StatusWarning, "No local filesystems could be inspected", false)
	}
	status := model.StatusOK
	issues := make([]string, 0)
	worst := values[0]
	for _, value := range values {
		if value.InodeUsed > worst.InodeUsed {
			worst = value
		}
		current := thresholdHigh(value.InodeUsed, cfg.Thresholds.InodeWarning, cfg.Thresholds.InodeCritical)
		status = higher(status, current)
		if current != model.StatusOK {
			issues = append(issues, fmt.Sprintf("%s %.1f%%", value.Mount.Path, value.InodeUsed))
		}
	}
	if status == model.StatusOK {
		return ok("filesystem.inode", "Inode usage", fmt.Sprintf("Highest usage is %.1f%% on %s", worst.InodeUsed, worst.Mount.Path))
	}
	return problem("filesystem.inode", "Inode usage", status, strings.Join(issues, "; "), status == model.StatusCritical)
}

func checkReadOnly(mounts []mount, err error) model.Result {
	if err != nil {
		return problem("filesystem.readonly", "Read-only filesystems", model.StatusWarning, "Host mount table could not be inspected", false)
	}
	if len(mounts) == 0 {
		return problem("filesystem.readonly", "Read-only filesystems", model.StatusWarning, "No local filesystems were found in the host mount table", false)
	}
	readonly := make([]string, 0)
	for _, item := range mounts {
		if item.ReadOnly {
			readonly = append(readonly, item.Path)
		}
	}
	if len(readonly) == 0 {
		return ok("filesystem.readonly", "Read-only filesystems", "All monitored local filesystems are writable")
	}
	return problem("filesystem.readonly", "Read-only filesystems", model.StatusCritical, strings.Join(readonly, ", "), true)
}

func readMemInfo() map[string]uint64 {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil
	}
	defer file.Close()
	values := map[string]uint64{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err == nil {
			values[strings.TrimSuffix(fields[0], ":")] = value
		}
	}
	return values
}

func checkMemory(values map[string]uint64, cfg config.Config) model.Result {
	total := values["MemTotal"]
	available := values["MemAvailable"]
	if total == 0 {
		return problem("memory.available", "Available memory", model.StatusWarning, "Unable to read /proc/meminfo", false)
	}
	percent := float64(available) / float64(total) * 100
	status := model.StatusOK
	if percent <= cfg.Thresholds.MemoryAvailableCritical {
		status = model.StatusCritical
	} else if percent <= cfg.Thresholds.MemoryAvailableWarning {
		status = model.StatusWarning
	}
	message := fmt.Sprintf("%.1f%% available (%s of %s)", percent, formatKiB(available), formatKiB(total))
	if status == model.StatusOK {
		return ok("memory.available", "Available memory", message)
	}
	return problem("memory.available", "Available memory", status, message, false)
}

func checkSwap(values map[string]uint64, cfg config.Config) model.Result {
	total := values["SwapTotal"]
	free := values["SwapFree"]
	if total == 0 {
		return ok("memory.swap", "Swap usage", "Swap is not configured")
	}
	used := total - free
	percent := float64(used) / float64(total) * 100
	status := thresholdHigh(percent, cfg.Thresholds.SwapWarning, cfg.Thresholds.SwapCritical)
	message := fmt.Sprintf("%.1f%% used (%s of %s)", percent, formatKiB(used), formatKiB(total))
	if status == model.StatusOK {
		return ok("memory.swap", "Swap usage", message)
	}
	return problem("memory.swap", "Swap usage", status, message, false)
}

func checkOOM(journal journalSnapshot) model.Result {
	if journal.Err != nil {
		return problem("memory.oom", "OOM kills", model.StatusWarning, journal.Err.Error(), false)
	}
	matches := filterLines(journal.Output, []string{"out of memory", "oom-kill", "killed process"})
	if len(matches) == 0 {
		return ok("memory.oom", "OOM kills", "No new OOM events")
	}
	return problem("memory.oom", "OOM kills", model.StatusCritical, joinLimited(matches, 4), true)
}

func checkLoad(cfg config.Config) model.Result {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return problem("cpu.load", "CPU load", model.StatusWarning, err.Error(), false)
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return problem("cpu.load", "CPU load", model.StatusWarning, "Invalid /proc/loadavg", false)
	}
	load1, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return problem("cpu.load", "CPU load", model.StatusWarning, err.Error(), false)
	}
	cpus := runtime.NumCPU()
	perCPU := load1 / float64(cpus)
	status := thresholdHigh(perCPU, cfg.Thresholds.LoadWarningPerCPU, cfg.Thresholds.LoadCriticalPerCPU)
	message := fmt.Sprintf("1-minute load %.2f across %d CPUs (%.2f per CPU)", load1, cpus, perCPU)
	if status == model.StatusOK {
		return ok("cpu.load", "CPU load", message)
	}
	return problem("cpu.load", "CPU load", status, message, false)
}

func checkKernelErrors(journal journalSnapshot) model.Result {
	if journal.Err != nil {
		return problem("kernel.errors", "Kernel and block-device errors", model.StatusWarning, journal.Err.Error(), false)
	}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bI/O error\b`),
		regexp.MustCompile(`(?i)buffer I/O error`),
		regexp.MustCompile(`(?i)EXT[234]-fs error`),
		regexp.MustCompile(`(?i)XFS.*(corruption|error)`),
		regexp.MustCompile(`(?i)blk_update_request`),
		regexp.MustCompile(`(?i)nvme.*(error|timeout|reset)`),
		regexp.MustCompile(`(?i)ata[0-9].*error`),
		regexp.MustCompile(`(?i)read-only file system`),
	}
	matches := make([]string, 0)
	for _, line := range nonEmptyLines(journal.Output) {
		for _, pattern := range patterns {
			if pattern.MatchString(line) {
				matches = append(matches, line)
				break
			}
		}
	}
	if len(matches) == 0 {
		return ok("kernel.errors", "Kernel and block-device errors", "No new matching kernel errors")
	}
	return problem("kernel.errors", "Kernel and block-device errors", model.StatusCritical, joinLimited(matches, 4), true)
}

func collectKernelJournal(ctx context.Context, cursorPath string, lastRun time.Time) journalSnapshot {
	cursorData, err := os.ReadFile(cursorPath)
	cursor := strings.TrimSpace(string(cursorData))
	if err != nil && !os.IsNotExist(err) {
		return journalSnapshot{Err: fmt.Errorf("read journal cursor: %w", err)}
	}
	if err := validateJournalCursor(cursor); err != nil {
		return journalSnapshot{Err: fmt.Errorf("invalid journal cursor: %w", err)}
	}

	baseArgs := []string{"--quiet", "--no-pager", "--output=short-iso", "--lines=2000", "--show-cursor"}
	args := append([]string{}, baseArgs...)
	if cursor != "" {
		args = append(args, "--after-cursor="+cursor)
	} else {
		if lastRun.IsZero() {
			lastRun = time.Now().Add(-10 * time.Minute)
		}
		args = append(args, "--since", "@"+strconv.FormatInt(lastRun.Unix(), 10))
	}
	args = append(args, "_TRANSPORT=kernel")

	result, runErr := runner.Run(ctx, 15*time.Second, "/usr/bin/journalctl", args...)
	if runErr != nil {
		return journalSnapshot{Err: runErr}
	}
	if result.Truncated {
		return journalSnapshot{Err: fmt.Errorf("journal output exceeded the safe collection limit")}
	}
	if result.ExitCode != 0 && cursor != "" {
		if lastRun.IsZero() {
			lastRun = time.Now().Add(-10 * time.Minute)
		}
		fallback := append([]string{}, baseArgs...)
		fallback = append(fallback, "--since", "@"+strconv.FormatInt(lastRun.Unix(), 10), "_TRANSPORT=kernel")
		result, runErr = runner.Run(ctx, 15*time.Second, "/usr/bin/journalctl", fallback...)
		if runErr != nil {
			return journalSnapshot{Err: runErr}
		}
		if result.Truncated {
			return journalSnapshot{Err: fmt.Errorf("journal output exceeded the safe collection limit")}
		}
	}
	if result.ExitCode != 0 {
		return journalSnapshot{Err: fmt.Errorf("journalctl failed: %s", result.Output)}
	}

	output, newCursor := parseJournalOutput(result.Output, cursor)
	if err := validateJournalCursor(newCursor); err != nil {
		return journalSnapshot{Err: fmt.Errorf("journalctl returned an invalid cursor: %w", err)}
	}
	return journalSnapshot{Output: output, Cursor: newCursor}
}

func validateJournalCursor(cursor string) error {
	if cursor == "" {
		return nil
	}
	if len(cursor) > 16*1024 {
		return fmt.Errorf("cursor exceeds 16 KiB")
	}
	for _, character := range cursor {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("cursor contains control characters")
		}
	}
	return nil
}

func parseJournalOutput(output, currentCursor string) (string, string) {
	lines := strings.Split(output, "\n")
	newCursor := currentCursor
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "-- cursor: ") {
			newCursor = strings.TrimSpace(strings.TrimPrefix(trimmed, "-- cursor: "))
			continue
		}
		if trimmed != "" {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n"), newCursor
}

func checkAPT(ctx context.Context) model.Result {
	result, err := runner.Run(ctx, 30*time.Second, "/usr/bin/apt-get", "-o", "Debug::NoLocking=1", "-o", "Dir::Cache::pkgcache=/dev/null", "-o", "Dir::Cache::srcpkgcache=/dev/null", "check")
	if err != nil {
		return problem("apt.check", "APT dependency check", model.StatusWarning, err.Error(), false)
	}
	if result.ExitCode == 0 {
		return ok("apt.check", "APT dependency check", "APT dependency state is consistent")
	}
	return problem("apt.check", "APT dependency check", model.StatusCritical, compactOutput(result.Output), true)
}

func checkDPKG(ctx context.Context) model.Result {
	result, err := runner.Run(ctx, 20*time.Second, "/usr/bin/dpkg", "--audit")
	if err != nil {
		return problem("dpkg.audit", "DPKG audit", model.StatusWarning, err.Error(), false)
	}
	if result.ExitCode == 0 && strings.TrimSpace(result.Output) == "" {
		return ok("dpkg.audit", "DPKG audit", "No partially installed or inconsistent packages")
	}
	return problem("dpkg.audit", "DPKG audit", model.StatusWarning, compactOutput(result.Output), false)
}

func checkAPTTimers(ctx context.Context) model.Result {
	units := []string{"apt-daily.timer", "apt-daily-upgrade.timer"}
	issues := make([]string, 0)
	for _, unit := range units {
		active := systemctlState(ctx, "is-active", unit)
		enabled := systemctlState(ctx, "is-enabled", unit)
		if active != "active" || enabled != "enabled" {
			issues = append(issues, fmt.Sprintf("%s active=%s enabled=%s", unit, active, enabled))
		}
	}
	if len(issues) == 0 {
		return ok("apt.timers", "APT timers", "apt-daily.timer and apt-daily-upgrade.timer are active and enabled")
	}
	return problem("apt.timers", "APT timers", model.StatusWarning, strings.Join(issues, "; "), false)
}

func checkUnattendedUpgrades(ctx context.Context, cfg config.Config, lastRun time.Time) model.Result {
	if _, err := os.Stat("/usr/bin/unattended-upgrade"); err != nil {
		return problem("apt.unattended", "Unattended upgrades", model.StatusWarning, "unattended-upgrades is not installed", false)
	}
	status := model.StatusOK
	issues := make([]string, 0)
	info, stamp := newestFileInfo([]string{
		"/var/lib/apt/periodic/unattended-upgrades-stamp",
		"/var/lib/apt/periodic/upgrade-stamp",
	})
	if info == nil {
		status = model.StatusWarning
		issues = append(issues, "successful upgrade stamp is missing")
	} else {
		age := time.Since(info.ModTime())
		maxAge := time.Duration(cfg.Thresholds.UnattendedUpgradeMaxAgeHours) * time.Hour
		if age > maxAge*2 {
			status = model.StatusCritical
			issues = append(issues, fmt.Sprintf("last successful run was %s ago", humanDuration(age)))
		} else if age > maxAge {
			status = higher(status, model.StatusWarning)
			issues = append(issues, fmt.Sprintf("last successful run was %s ago", humanDuration(age)))
		}
	}
	serviceResult := systemctlProperty(ctx, "apt-daily-upgrade.service", "Result")
	if serviceResult != "" && serviceResult != "success" {
		status = model.StatusCritical
		issues = append(issues, "last apt-daily-upgrade result="+serviceResult)
	}
	if lastRun.IsZero() {
		lastRun = time.Now().Add(-10 * time.Minute)
	}
	journal, err := runner.Run(ctx, 15*time.Second, "/usr/bin/journalctl", "--quiet", "--no-pager", "--unit=apt-daily-upgrade.service", "--since", "@"+strconv.FormatInt(lastRun.Unix(), 10))
	if err == nil && journal.ExitCode == 0 {
		matches := filterLines(journal.Output, []string{"error", "failed", "traceback"})
		if len(matches) > 0 {
			status = model.StatusCritical
			issues = append(issues, joinLimited(matches, 2))
		}
	}
	if status == model.StatusOK {
		if info != nil {
			return ok("apt.unattended", "Unattended upgrades", fmt.Sprintf("Last successful run was %s ago (%s)", humanDuration(time.Since(info.ModTime())), stamp))
		}
		return ok("apt.unattended", "Unattended upgrades", "No errors detected")
	}
	return problem("apt.unattended", "Unattended upgrades", status, strings.Join(issues, "; "), status == model.StatusCritical)
}

func checkRebootRequired() model.Result {
	data, err := os.ReadFile("/var/run/reboot-required")
	if os.IsNotExist(err) {
		return ok("reboot.required", "Reboot required", "No reboot is required")
	}
	if err != nil {
		return problem("reboot.required", "Reboot required", model.StatusWarning, err.Error(), false)
	}
	message := strings.TrimSpace(string(data))
	if message == "" {
		message = "A reboot is required"
	}
	if info, err := os.Stat("/var/run/reboot-required"); err == nil {
		message = fmt.Sprintf("%s; marker age %s", message, humanDuration(time.Since(info.ModTime())))
	}
	return problem("reboot.required", "Reboot required", model.StatusWarning, message, false)
}

func checkNeedrestart(ctx context.Context) model.Result {
	if _, err := os.Stat("/usr/sbin/needrestart"); err != nil {
		return problem("kernel.needrestart", "Running kernel", model.StatusWarning, "needrestart is not installed", false)
	}
	result, err := runner.Run(ctx, 30*time.Second, "/usr/sbin/needrestart", "-b", "-k")
	if err != nil {
		return problem("kernel.needrestart", "Running kernel", model.StatusWarning, err.Error(), false)
	}
	values := map[string]string{}
	for _, line := range nonEmptyLines(result.Output) {
		key, value, ok := strings.Cut(line, ":")
		if ok {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	current := values["NEEDRESTART-KCUR"]
	expected := values["NEEDRESTART-KEXP"]
	statusCode := values["NEEDRESTART-KSTA"]
	switch statusCode {
	case "1":
		return ok("kernel.needrestart", "Running kernel", fmt.Sprintf("Running kernel %s is current", current))
	case "2":
		return problem("kernel.needrestart", "Running kernel", model.StatusWarning, fmt.Sprintf("Kernel update pending: running %s, expected %s", current, expected), false)
	case "3":
		return problem("kernel.needrestart", "Running kernel", model.StatusWarning, fmt.Sprintf("Kernel version update pending: running %s, expected %s", current, expected), false)
	default:
		return problem("kernel.needrestart", "Running kernel", model.StatusWarning, "Unable to determine kernel status from needrestart", false)
	}
}

func checkTimeSync(ctx context.Context) model.Result {
	result, err := runner.Run(ctx, 10*time.Second, "/usr/bin/timedatectl", "show", "--property=NTPSynchronized", "--value")
	if err != nil {
		return problem("time.sync", "Time synchronization", model.StatusWarning, err.Error(), false)
	}
	if result.ExitCode == 0 && strings.TrimSpace(result.Output) == "yes" {
		return ok("time.sync", "Time synchronization", "NTP synchronization is active")
	}
	return problem("time.sync", "Time synchronization", model.StatusWarning, "NTPSynchronized="+strings.TrimSpace(result.Output), false)
}

func checkAgentTimer(ctx context.Context) model.Result {
	load := systemctlProperty(ctx, "gotify-vps-agent.timer", "LoadState")
	active := systemctlState(ctx, "is-active", "gotify-vps-agent.timer")
	enabled := systemctlState(ctx, "is-enabled", "gotify-vps-agent.timer")
	nextRealtime := systemctlProperty(ctx, "gotify-vps-agent.timer", "NextElapseUSecRealtime")
	nextMonotonic := systemctlProperty(ctx, "gotify-vps-agent.timer", "NextElapseUSecMonotonic")
	scheduled := nextRealtime != "" && nextRealtime != "0" && nextRealtime != "n/a"
	scheduled = scheduled || (nextMonotonic != "" && nextMonotonic != "0" && nextMonotonic != "n/a")
	if load == "loaded" && active == "active" && enabled == "enabled" && scheduled {
		return ok("agent.timer", "Agent timer", "gotify-vps-agent.timer is active, enabled and scheduled")
	}
	return problem("agent.timer", "Agent timer", model.StatusWarning, fmt.Sprintf("load=%s active=%s enabled=%s scheduled=%t", load, active, enabled, scheduled), false)
}

func DeliveryQueue(queued, attempts int, oldest time.Time) model.Result {
	if queued == 0 {
		return ok("delivery.queue", "Gotify delivery queue", "No undelivered notifications")
	}
	status := model.StatusWarning
	if attempts >= 5 || (!oldest.IsZero() && time.Since(oldest) >= 24*time.Hour) {
		status = model.StatusCritical
	}
	return problem("delivery.queue", "Gotify delivery queue", status, "Gotify delivery queue is not empty", false)
}

func checkAgentFreshness(cfg config.Config, lastRun time.Time) model.Result {
	if lastRun.IsZero() {
		return ok("agent.freshness", "Agent execution freshness", "No previous run is expected during initial setup")
	}
	interval, err := time.ParseDuration(cfg.Agent.Interval)
	if err != nil {
		return problem("agent.freshness", "Agent execution freshness", model.StatusWarning, "Unable to parse configured interval", false)
	}
	age := time.Since(lastRun)
	if age > interval*6 {
		return problem("agent.freshness", "Agent execution freshness", model.StatusCritical, fmt.Sprintf("Previous completed run was %s ago", humanDuration(age)), true)
	}
	if age > interval*3 {
		return problem("agent.freshness", "Agent execution freshness", model.StatusWarning, fmt.Sprintf("Previous completed run was %s ago", humanDuration(age)), false)
	}
	return ok("agent.freshness", "Agent execution freshness", fmt.Sprintf("Previous completed run was %s ago", humanDuration(age)))
}

func checkServices(ctx context.Context, configured map[string]string) []model.Result {
	ids := make([]string, 0, len(configured))
	for id := range configured {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	results := make([]model.Result, 0, len(ids))
	for _, id := range ids {
		unit := configured[id]
		if id == "postgresql" {
			results = append(results, checkPostgreSQL(ctx, unit))
			continue
		}
		active := systemctlState(ctx, "is-active", unit)
		title := serviceTitle(id)
		if active == "active" {
			results = append(results, ok("service."+id, title, unit+" is active"))
		} else {
			results = append(results, problem("service."+id, title, model.StatusCritical, fmt.Sprintf("%s is %s", unit, active), true))
		}
	}
	return results
}

func checkPostgreSQL(ctx context.Context, unit string) model.Result {
	if _, err := os.Stat("/usr/bin/pg_lsclusters"); err != nil {
		active := systemctlState(ctx, "is-active", unit)
		if active == "active" {
			return ok("service.postgresql", "PostgreSQL service", unit+" is active")
		}
		return problem("service.postgresql", "PostgreSQL service", model.StatusCritical, fmt.Sprintf("%s is %s", unit, active), true)
	}
	result, err := runner.Run(ctx, 10*time.Second, "/usr/bin/pg_lsclusters", "--no-header")
	if err != nil {
		return problem("service.postgresql", "PostgreSQL service", model.StatusWarning, err.Error(), false)
	}
	if result.ExitCode != 0 {
		return problem("service.postgresql", "PostgreSQL service", model.StatusWarning, compactOutput(result.Output), false)
	}
	lines := nonEmptyLines(result.Output)
	if len(lines) == 0 {
		return problem("service.postgresql", "PostgreSQL service", model.StatusWarning, "No PostgreSQL clusters were found", false)
	}
	offline := make([]string, 0)
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			offline = append(offline, line)
			continue
		}
		if fields[3] != "online" {
			offline = append(offline, strings.Join(fields[:4], " "))
		}
	}
	if len(offline) > 0 {
		return problem("service.postgresql", "PostgreSQL service", model.StatusCritical, "Offline clusters: "+joinLimited(offline, 4), true)
	}
	return ok("service.postgresql", "PostgreSQL service", fmt.Sprintf("%d PostgreSQL cluster(s) online", len(lines)))
}

func serviceTitle(id string) string {
	names := map[string]string{
		"ssh": "SSH service", "nginx": "Nginx service", "php-fpm": "PHP-FPM service",
		"mariadb": "MariaDB/MySQL service", "postgresql": "PostgreSQL service", "tor": "Tor service",
	}
	if value := names[id]; value != "" {
		return value
	}
	return id + " service"
}

func systemctlState(ctx context.Context, command, unit string) string {
	result, err := runner.Run(ctx, 8*time.Second, "/usr/bin/systemctl", command, unit)
	if err != nil {
		return "unknown"
	}
	value := strings.TrimSpace(result.Output)
	if value == "" {
		return "unknown"
	}
	return value
}

func systemctlProperty(ctx context.Context, unit, property string) string {
	result, err := runner.Run(ctx, 8*time.Second, "/usr/bin/systemctl", "show", unit, "--property="+property, "--value")
	if err != nil || result.ExitCode != 0 {
		return ""
	}
	return strings.TrimSpace(result.Output)
}

func thresholdHigh(value, warning, critical float64) model.Status {
	if value >= critical {
		return model.StatusCritical
	}
	if value >= warning {
		return model.StatusWarning
	}
	return model.StatusOK
}

func higher(left, right model.Status) model.Status {
	order := map[model.Status]int{model.StatusOK: 0, model.StatusWarning: 1, model.StatusCritical: 2}
	if order[right] > order[left] {
		return right
	}
	return left
}

func ok(id, title, message string) model.Result {
	return model.Result{ID: id, Title: title, Status: model.StatusOK, Message: limitMessage(message)}
}

func problem(id, title string, status model.Status, message string, immediate bool) model.Result {
	if strings.TrimSpace(message) == "" {
		message = "No diagnostic output"
	}
	return model.Result{ID: id, Title: title, Status: status, Message: limitMessage(message), Immediate: immediate}
}

func limitMessage(value string) string {
	value = textsafe.Sanitize(strings.TrimSpace(value), 4096)
	if value == "" {
		return "No diagnostic output"
	}
	return value
}

func newestFileInfo(paths []string) (os.FileInfo, string) {
	var newest os.FileInfo
	selected := ""
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if newest == nil || info.ModTime().After(newest.ModTime()) {
			newest = info
			selected = path
		}
	}
	return newest, selected
}

func nonEmptyLines(output string) []string {
	lines := make([]string, 0)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func filterLines(output string, needles []string) []string {
	matches := make([]string, 0)
	for _, line := range nonEmptyLines(output) {
		lower := strings.ToLower(line)
		for _, needle := range needles {
			if strings.Contains(lower, strings.ToLower(needle)) {
				matches = append(matches, line)
				break
			}
		}
	}
	return matches
}

func joinLimited(lines []string, limit int) string {
	if len(lines) <= limit {
		return strings.Join(lines, "; ")
	}
	return strings.Join(lines[:limit], "; ") + fmt.Sprintf("; and %d more", len(lines)-limit)
}

func compactOutput(output string) string {
	lines := nonEmptyLines(output)
	if len(lines) == 0 {
		return "Command failed without output"
	}
	return joinLimited(lines, 6)
}

func formatKiB(value uint64) string {
	bytes := float64(value * 1024)
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	unit := 0
	for bytes >= 1024 && unit < len(units)-1 {
		bytes /= 1024
		unit++
	}
	return fmt.Sprintf("%.1f %s", bytes, units[unit])
}

func humanDuration(value time.Duration) string {
	if value < 0 {
		value = -value
	}
	if value >= 24*time.Hour {
		return fmt.Sprintf("%.1f days", value.Hours()/24)
	}
	if value >= time.Hour {
		return fmt.Sprintf("%.1f hours", value.Hours())
	}
	return fmt.Sprintf("%.0f minutes", value.Minutes())
}

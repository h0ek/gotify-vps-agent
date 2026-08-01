package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/h0ek/gotify-vps-agent/internal/runner"
)

type Profile struct {
	ID          string
	Name        string
	Candidates  []string
	UnitGlob    string
	Packages    []string
	BinaryPaths []string
	BinaryGlobs []string
}

type Detection struct {
	Profile  Profile
	Unit     string
	Active   bool
	Enabled  bool
	Evidence []string
}

func Profiles() []Profile {
	return []Profile{
		{ID: "ssh", Name: "SSH", Candidates: []string{"ssh.service"}, Packages: []string{"openssh-server"}, BinaryPaths: []string{"/usr/sbin/sshd"}},
		{ID: "nginx", Name: "Nginx", Candidates: []string{"nginx.service"}, Packages: []string{"nginx"}, BinaryPaths: []string{"/usr/sbin/nginx"}},
		{ID: "php-fpm", Name: "PHP-FPM", UnitGlob: "php*-fpm.service", BinaryGlobs: []string{"/usr/sbin/php-fpm*"}},
		{ID: "mariadb", Name: "MariaDB/MySQL", Candidates: []string{"mariadb.service", "mysql.service"}, Packages: []string{"mariadb-server", "mysql-server"}, BinaryPaths: []string{"/usr/sbin/mariadbd", "/usr/sbin/mysqld"}},
		{ID: "postgresql", Name: "PostgreSQL", Candidates: []string{"postgresql.service"}, Packages: []string{"postgresql"}, BinaryPaths: []string{"/usr/bin/pg_lsclusters"}},
		{ID: "tor", Name: "Tor", Candidates: []string{"tor@default.service", "tor.service"}, Packages: []string{"tor"}, BinaryPaths: []string{"/usr/bin/tor"}},
	}
}

func Detect(ctx context.Context) []Detection {
	detections := make([]Detection, 0)
	for _, profile := range Profiles() {
		unit := ""
		for _, candidate := range profile.Candidates {
			if unitExists(ctx, candidate) {
				unit = candidate
				break
			}
		}
		if unit == "" && profile.UnitGlob != "" {
			unit = detectGlob(ctx, profile.UnitGlob)
		}
		if unit == "" {
			continue
		}
		evidence := []string{"systemd:" + unit}
		for _, packageName := range profile.Packages {
			if packageInstalled(ctx, packageName) {
				evidence = append(evidence, "package:"+packageName)
			}
		}
		for _, path := range profile.BinaryPaths {
			if regularExecutable(path) {
				evidence = append(evidence, "binary:"+path)
			}
		}
		for _, pattern := range profile.BinaryGlobs {
			matches, _ := filepath.Glob(pattern)
			sort.Strings(matches)
			for _, path := range matches {
				if regularExecutable(path) {
					evidence = append(evidence, "binary:"+path)
					break
				}
			}
		}
		detections = append(detections, Detection{
			Profile:  profile,
			Unit:     unit,
			Active:   unitState(ctx, "is-active", unit) == "active",
			Enabled:  unitState(ctx, "is-enabled", unit) == "enabled",
			Evidence: evidence,
		})
	}
	return detections
}

func ProfileByID(id string) (Profile, bool) {
	for _, profile := range Profiles() {
		if profile.ID == id {
			return profile, true
		}
	}
	return Profile{}, false
}

func DetectByID(ctx context.Context, id string) (Detection, error) {
	if _, ok := ProfileByID(id); !ok {
		return Detection{}, fmt.Errorf("unsupported service profile %q", id)
	}
	for _, detection := range Detect(ctx) {
		if detection.Profile.ID == id {
			return detection, nil
		}
	}
	return Detection{}, fmt.Errorf("service %q was not detected", id)
}

func detectGlob(ctx context.Context, pattern string) string {
	paths := []string{"/usr/lib/systemd/system", "/lib/systemd/system", "/etc/systemd/system"}
	units := make([]string, 0)
	seen := map[string]bool{}
	for _, base := range paths {
		matches, _ := filepath.Glob(filepath.Join(base, pattern))
		for _, match := range matches {
			unit := filepath.Base(match)
			if !seen[unit] && unitExists(ctx, unit) {
				seen[unit] = true
				units = append(units, unit)
			}
		}
	}
	sort.Strings(units)
	for i := len(units) - 1; i >= 0; i-- {
		if unitState(ctx, "is-active", units[i]) == "active" {
			return units[i]
		}
	}
	if len(units) == 0 {
		return ""
	}
	return units[len(units)-1]
}

func unitExists(ctx context.Context, unit string) bool {
	result, err := runner.Run(ctx, 5*time.Second, "/usr/bin/systemctl", "show", unit, "--property=LoadState", "--value")
	return err == nil && result.ExitCode == 0 && strings.TrimSpace(result.Output) == "loaded"
}

func unitState(ctx context.Context, command, unit string) string {
	result, err := runner.Run(ctx, 5*time.Second, "/usr/bin/systemctl", command, unit)
	if err != nil {
		return "unknown"
	}
	value := strings.TrimSpace(result.Output)
	if value == "" {
		return "unknown"
	}
	return value
}

func packageInstalled(ctx context.Context, name string) bool {
	result, err := runner.Run(ctx, 5*time.Second, "/usr/bin/dpkg-query", "-W", "-f=${db:Status-Abbrev}", name)
	return err == nil && result.ExitCode == 0 && strings.HasPrefix(strings.TrimSpace(result.Output), "ii ")
}

func regularExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0
}

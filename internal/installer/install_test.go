package installer

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func readFixture(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func sandboxScript(t *testing.T, name, root string) string {
	t.Helper()
	content := readFixture(t, filepath.Join("..", "..", "scripts", name))
	replacer := strings.NewReplacer(
		"/usr/local", root+"/usr/local",
		"/var/lib/", root+"/var/lib/",
		"/etc/", root+"/etc/",
		"/run/systemd/system", root+"/run/systemd/system",
		"${EUID}", "0",
		"-o root -g root", "",
	)
	return replacer.Replace(content)
}

func installerFixture(t *testing.T, active, enabled bool) (string, []string) {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"tmp", "run/systemd/system", "etc/systemd/system", "var/lib/gotify-vps-agent"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	writeFixture(t, filepath.Join(root, "etc/os-release"), "ID=debian\nVERSION_ID=13\n", 0644)
	writeFixture(t, filepath.Join(root, "etc/gotify-vps-agent/config.toml"), "original configuration\n", 0644)
	writeFixture(t, filepath.Join(root, "etc/gotify-vps-agent/gotify.token"), "secret-token\n", 0600)
	writeFixture(t, filepath.Join(root, "var/lib/gotify-vps-agent/state.json"), "original state\n", 0600)
	writeFixture(t, filepath.Join(root, "etc/systemd/system/gotify-vps-agent.service"), "original service\n", 0644)
	for _, version := range []string{"0.2.0", "0.2.1"} {
		path := filepath.Join(root, "new-agent")
		if version == "0.2.0" {
			path = filepath.Join(root, "usr/local/bin/gotify-vps-agent")
		}
		writeFixture(t, path, "#!/usr/bin/env bash\nif [[ \"$1\" == version ]]; then printf 'gotify-vps-agent "+version+" commit=test built=test\\n'; fi\n", 0755)
	}
	writeFixture(t, filepath.Join(root, "active"), fmt.Sprintf("%t", active), 0600)
	writeFixture(t, filepath.Join(root, "enabled"), fmt.Sprintf("%t", enabled), 0600)
	writeFixture(t, filepath.Join(root, "bin/systemctl"), `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$GVA_TEST_ROOT/calls"
case "$1" in
  is-active) [[ "$(cat "$GVA_TEST_ROOT/active")" == true ]] ;;
  is-enabled) [[ "$(cat "$GVA_TEST_ROOT/enabled")" == true ]] ;;
  stop) if [[ "$2" == gotify-vps-agent.timer ]]; then printf false > "$GVA_TEST_ROOT/active"; fi ;;
  start|restart) printf true > "$GVA_TEST_ROOT/active" ;;
  enable) printf true > "$GVA_TEST_ROOT/enabled" ;;
  disable) printf false > "$GVA_TEST_ROOT/enabled"; printf false > "$GVA_TEST_ROOT/active" ;;
esac
`, 0755)
	writeFixture(t, filepath.Join(root, "bin/systemd-analyze"), "#!/usr/bin/env bash\n[[ \"${GVA_TEST_FAIL_VERIFY:-0}\" != 1 ]]\n", 0755)
	writeFixture(t, filepath.Join(root, "scripts/install.sh"), sandboxScript(t, "install.sh", root), 0755)
	env := append(os.Environ(), "GVA_TEST_ROOT="+root, "TMPDIR="+filepath.Join(root, "tmp"), "PATH="+filepath.Join(root, "bin")+":"+os.Getenv("PATH"))
	return root, env
}

func TestUpgradePreservesTimerActivityAndEnablement(t *testing.T) {
	for _, active := range []bool{false, true} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("active=%t,enabled=%t", active, enabled), func(t *testing.T) {
				root, env := installerFixture(t, active, enabled)
				command := exec.Command("bash", filepath.Join(root, "scripts/install.sh"), "--local", filepath.Join(root, "new-agent"), "--upgrade")
				command.Env = env
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("upgrade failed: %v\n%s", err, output)
				}
				if readFixture(t, filepath.Join(root, "active")) != fmt.Sprintf("%t", active) || readFixture(t, filepath.Join(root, "enabled")) != fmt.Sprintf("%t", enabled) {
					t.Fatal("upgrade changed timer activity or enablement")
				}
				if readFixture(t, filepath.Join(root, "etc/gotify-vps-agent/config.toml")) != "original configuration\n" || readFixture(t, filepath.Join(root, "var/lib/gotify-vps-agent/state.json")) != "original state\n" {
					t.Fatal("upgrade changed configuration or runtime state")
				}
				if !strings.Contains(readFixture(t, filepath.Join(root, "usr/local/bin/gotify-vps-agent")), "0.2.1") {
					t.Fatal("new binary was not installed")
				}
			})
		}
	}
}

func TestFailedUpgradeRestoresFilesAndRunningTimer(t *testing.T) {
	root, env := installerFixture(t, true, false)
	command := exec.Command("bash", filepath.Join(root, "scripts/install.sh"), "--local", filepath.Join(root, "new-agent"), "--upgrade")
	command.Env = append(env, "GVA_TEST_FAIL_VERIFY=1")
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("expected verification failure, got success: %s", output)
	}
	if !strings.Contains(readFixture(t, filepath.Join(root, "usr/local/bin/gotify-vps-agent")), "0.2.0") || readFixture(t, filepath.Join(root, "etc/systemd/system/gotify-vps-agent.service")) != "original service\n" {
		t.Fatal("rollback did not restore original files")
	}
	if readFixture(t, filepath.Join(root, "active")) != "true" || readFixture(t, filepath.Join(root, "enabled")) != "false" {
		t.Fatal("rollback did not restore the active disabled timer")
	}
}

func TestUninstallOnlyResetsAgentUnits(t *testing.T) {
	for _, standalone := range []bool{false, true} {
		t.Run(fmt.Sprintf("standalone=%t", standalone), func(t *testing.T) {
			root, env := installerFixture(t, true, true)
			command := exec.Command("bash", filepath.Join(root, "scripts/install.sh"), "--local", filepath.Join(root, "new-agent"), "--upgrade")
			command.Env = env
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("upgrade failed: %v\n%s", err, output)
			}
			uninstaller := filepath.Join(root, "usr/local/lib/gotify-vps-agent/uninstall.sh")
			if standalone {
				writeFixture(t, uninstaller, sandboxScript(t, "uninstall.sh", root), 0750)
			}
			command = exec.Command("bash", uninstaller, "--yes")
			command.Env = env
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("uninstall failed: %v\n%s", err, output)
			}
			for _, line := range strings.Split(readFixture(t, filepath.Join(root, "calls")), "\n") {
				if strings.HasPrefix(line, "reset-failed") && line != "reset-failed gotify-vps-agent.service gotify-vps-agent.timer" {
					t.Fatalf("uninstall resets unrelated units: %s", line)
				}
			}
			if readFixture(t, filepath.Join(root, "etc/gotify-vps-agent/gotify.token")) != "secret-token\n" {
				t.Fatal("uninstall removed retained configuration")
			}
		})
	}
}

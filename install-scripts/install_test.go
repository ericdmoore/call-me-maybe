package installscripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func bash(t *testing.T, script string, env ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestDryRunsNeverExecuteHostCommands(t *testing.T) {
	root := t.TempDir()
	// A dry run may inspect files but must never call these programs, even
	// for probing package availability or checking root/systemd privileges.
	for _, name := range []string{"apt-get", "apt-cache", "dnf", "pacman", "useradd", "install", "rsync", "systemctl", "getent", "uname", "id"} {
		err := os.WriteFile(filepath.Join(root, name), []byte("#!/bin/sh\necho UNEXPECTED_HOST_COMMAND >&2\nexit 99\n"), 0755)
		if err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(root, "doorman binary")
	if err := os.WriteFile(binary, []byte("must not execute"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, distro := range []string{"ubuntu", "debian", "centos", "fedora", "arch"} {
		t.Run(distro, func(t *testing.T) {
			out, err := bash(t, `bash "$DISTRO.sh" --dry-run --existing-asterisk --binary "$BINARY"`, "DISTRO="+distro, "BINARY="+binary, "PATH="+root+":"+os.Getenv("PATH"))
			if err != nil || strings.Contains(out, "UNEXPECTED_HOST_COMMAND") {
				t.Fatalf("%v\n%s", err, out)
			}
			for _, want := range []string{"useradd", "daemon-reload", "/opt/call-me-maybe/bin/doorman", "/var/lib/doorman/journal", "No doorman service was started"} {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q in %s", want, out)
				}
			}
			if strings.Contains(out, "enable --now") || strings.Contains(out, "setenforce") {
				t.Fatal("unexpected activation/security change")
			}
		})
	}
}

func TestArgumentsAndHelp(t *testing.T) {
	for _, args := range []string{"--unknown", "--binary", "--dry-run", "--dry-run --binary /no/such/file"} {
		out, err := bash(t, "bash ubuntu.sh "+args)
		if err == nil || !strings.Contains(out, "error:") {
			t.Fatalf("%s: %v %s", args, err, out)
		}
	}
	for _, distro := range []string{"ubuntu", "debian", "centos", "fedora", "arch"} {
		out, err := bash(t, `bash "$DISTRO.sh" --help`, "DISTRO="+distro)
		if err != nil || !strings.Contains(out, "Usage:") {
			t.Fatalf("%v: %s", err, out)
		}
	}
}

func TestVersionGate(t *testing.T) {
	for _, tc := range []struct {
		version string
		ok      bool
	}{{"1:20.6.0~dfsg", true}, {"22.3.0", true}, {"18.26.4", false}, {"(none)", false}, {"garbage", false}} {
		out, err := bash(t, `source ./common.sh; require_version "$VERSION"`, "VERSION="+tc.version)
		if (err == nil) != tc.ok {
			t.Errorf("%s: %v %s", tc.version, err, out)
		}
	}
}

func TestOldPackageStopsBeforeInstallation(t *testing.T) {
	for _, distro := range []string{"ubuntu", "debian", "centos", "fedora"} {
		out, err := bash(t, `
source ./common.sh
dry_run=0; existing=0; profile=$DISTRO
apt-get() { if [ "$1" != update ]; then echo UNEXPECTED_INSTALL; fi; }
apt-cache() { echo '  Candidate: 1:18.26.4-1'; }
dnf() { if [ "$1" = install ]; then echo UNEXPECTED_INSTALL; else echo 18.26.4; fi; }
packages
`, "DISTRO="+distro)
		if err == nil || strings.Contains(out, "UNEXPECTED_INSTALL") || !strings.Contains(out, "too old") {
			t.Fatalf("%s: %v %s", distro, err, out)
		}
	}
}

func TestPackagePlans(t *testing.T) {
	for distro, want := range map[string]string{"ubuntu": "apt-get install -y asterisk", "debian": "apt-get install -y asterisk", "centos": "dnf install -y asterisk", "fedora": "dnf install -y asterisk"} {
		out, err := bash(t, `source ./common.sh; dry_run=1; existing=0; profile=$DISTRO; packages`, "DISTRO="+distro)
		if err != nil || !strings.Contains(out, want) {
			t.Fatalf("%s: %v %s", distro, err, out)
		}
	}
	out, err := bash(t, `source ./common.sh; dry_run=1; existing=0; profile=arch; packages`)
	if err == nil || !strings.Contains(out, "--existing-asterisk") {
		t.Fatalf("%v %s", err, out)
	}
}

func TestInstallPreservesExistingFiles(t *testing.T) {
	root := t.TempDir()
	source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
	if err := os.WriteFile(source, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	env := []string{"SOURCE=" + source, "TARGET=" + target}
	script := `source ./common.sh; dry_run=0; install_once "$SOURCE" "$TARGET" 0644`
	if out, err := bash(t, script, env...); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if out, err := bash(t, script, env...); err != nil {
		t.Fatalf("idempotence: %v %s", err, out)
	}
	if err := os.WriteFile(target, []byte("operator edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := bash(t, script, env...); err == nil {
		t.Fatalf("overwrote config: %s", out)
	}
	b, err := os.ReadFile(target)
	if err != nil || string(b) != "operator edit" {
		t.Fatalf("%v %q", err, b)
	}
}

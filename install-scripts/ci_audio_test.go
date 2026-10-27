package installscripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Run the actual bootstrap with a closed PATH. Incompatible cached packages
// must not be handed to dpkg or poison the fresh, bounded APT fallback.
func TestCIAudioPackageCacheUsesDependencyResolver(t *testing.T) {
	for _, scenario := range []string{"compatible", "stale", "empty", "fresh-failure"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			cache := filepath.Join(root, "cache")
			debs := filepath.Join(cache, "espeak-debs")
			if err := os.MkdirAll(bin, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(debs, 0700); err != nil {
				t.Fatal(err)
			}
			if scenario != "empty" {
				if err := os.WriteFile(filepath.Join(debs, "old.deb"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			write := func(name, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/bash\n"+body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"mkdir", "rm", "head", "id"} {
				path, err := exec.LookPath(name)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path, filepath.Join(bin, name)); err != nil {
					t.Fatal(err)
				}
			}
			write("ffmpeg", "echo 'ffmpeg fixture'\n")
			write("ffprobe", "echo 'ffprobe fixture'\n")
			write("sudo", "exec \"$@\"\n")
			write("timeout", "shift; exec \"$@\"\n")
			write("sleep", "exit 0\n")
			write("chown", "exit 0\n")
			write("dpkg", "echo forbidden-dpkg >> \"$TRACE\"; exit 99\n")
			write("apt-get", `echo "$*" >> "$TRACE"
case " $* " in
 *" update "*) exit 0;;
 *" --download-only "*)
   for arg in "$@"; do case "$arg" in Dir::Cache::archives=*) dir=${arg#*=};; esac; done
   : > "$dir/fresh.deb"
   exit 0;;
 *" --no-download "*)
   case " $* " in
     *old.deb*) [ "$SCENARIO" = compatible ] || exit 100;;
     *fresh.deb*) [ "$SCENARIO" != fresh-failure ] || exit 100;;
     *) exit 98;;
   esac;;
 *" --fix-broken "*) echo plain-fallback >> "$TRACE";;
 *) exit 97;;
esac
printf '#!/bin/bash\necho espeak-fixture\n' > "$FIXTURE_BIN/espeak-ng"
chmod +x "$FIXTURE_BIN/espeak-ng"
`)
			// chmod is used only by the fake successful package install.
			chmod, err := exec.LookPath("chmod")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(chmod, filepath.Join(bin, "chmod")); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(root, "trace")
			cmd := exec.Command("bash", "../scripts/ci-audio-tools.sh", cache)
			cmd.Env = append(os.Environ(), "PATH="+bin, "TRACE="+log, "SCENARIO="+scenario, "FIXTURE_BIN="+bin, "GITHUB_PATH=")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("bootstrap failed: %v\n%s", err, out)
			}
			trace, _ := os.ReadFile(log)
			text := string(trace)
			if strings.Contains(text, "forbidden-dpkg") || !strings.Contains(text, "--no-download --no-remove") {
				t.Fatalf("unsafe cache installation:\n%s", text)
			}
			if scenario == "compatible" && strings.Contains(text, "--download-only") {
				t.Fatal("compatible cache needed a mirror")
			}
			if scenario != "compatible" && !strings.Contains(text, "--download-only") {
				t.Fatal("stale/empty cache did not refresh")
			}
			if scenario == "fresh-failure" && !strings.Contains(text, "plain-fallback") {
				t.Fatal("failed fresh cache did not use bounded fallback")
			}
		})
	}
}

func TestCIAudioCacheIdentityComesFromActualRunnerImage(t *testing.T) {
	for _, file := range []string{"../.github/workflows/ci.yml", "../.github/workflows/release.yml"} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if strings.Contains(text, "${{ env.ImageOS }}") || !strings.Contains(text, ". /etc/os-release") || !strings.Contains(text, "${{ steps.audio-image.outputs.identity }}") {
			t.Fatalf("%s can share cached packages across image versions", file)
		}
	}
}

package installscripts

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Run the complete release installer with a closed PATH and local download
// fixtures. Failed verification must neither execute the payload nor replace
// an existing installation, even when --force was requested.
func TestReleaseDownloadVerification(t *testing.T) {
	payload := "#!/bin/sh\nprintf 'executed\\n' >> \"$EXECUTED\"\nprintf 'doorman 0.9.9\\n'\n"
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
	entry := digest + "  doorman-linux-amd64\n"
	for _, tc := range []struct {
		name, manifest, tool, toolMode string
		missingManifest, downloadFail  bool
		ok                             bool
	}{
		{name: "sha256sum", manifest: entry, tool: "sha256sum", ok: true},
		{name: "shasum fallback", manifest: entry, tool: "shasum", ok: true},
		{name: "binary marker and uppercase", manifest: strings.ToUpper(digest) + " *doorman-linux-amd64\n", tool: "sha256sum", ok: true},
		{name: "missing manifest", missingManifest: true, tool: "sha256sum"},
		{name: "empty manifest", tool: "sha256sum"},
		{name: "wrong asset", manifest: digest + "  doorman-linux-arm64\n", tool: "sha256sum"},
		{name: "malformed digest", manifest: strings.Repeat("z", 64) + "  doorman-linux-amd64\n", tool: "sha256sum"},
		{name: "short digest", manifest: "abcd  doorman-linux-amd64\n", tool: "sha256sum"},
		{name: "extra fields", manifest: strings.TrimSpace(entry) + " unexpected\n", tool: "sha256sum"},
		{name: "duplicate entry", manifest: entry + entry, tool: "sha256sum"},
		{name: "conflicting duplicate", manifest: entry + strings.Repeat("0", 64) + " *doorman-linux-amd64\n", tool: "sha256sum"},
		{name: "digest mismatch", manifest: strings.Repeat("0", 64) + "  doorman-linux-amd64\n", tool: "sha256sum"},
		{name: "no hash tool", manifest: entry},
		{name: "sha256sum fails after output", manifest: entry, tool: "sha256sum", toolMode: "fail"},
		{name: "shasum fails after output", manifest: entry, tool: "shasum", toolMode: "fail"},
		{name: "invalid tool output", manifest: entry, tool: "sha256sum", toolMode: "invalid"},
		{name: "partial download", manifest: entry, tool: "sha256sum", downloadFail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			bin, prefix, temp := filepath.Join(root, "tools"), filepath.Join(root, "installed"), filepath.Join(root, "tmp")
			for _, dir := range []string{bin, prefix, temp} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path, contents string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(contents), 0700); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"mktemp", "mkdir", "tr", "awk", "rm", "chmod", "mv", "dirname", "cp", "cat"} {
				path, err := exec.LookPath(name)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path, filepath.Join(bin, name)); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(bin, "uname"), "#!/bin/sh\ncase \"$1\" in -s) echo Linux;; -m) echo x86_64;; *) exit 1;; esac\n")
			write(filepath.Join(bin, "asterisk"), "#!/bin/sh\nexit 0\n")
			write(filepath.Join(root, "payload"), payload)
			write(filepath.Join(root, "manifest"), tc.manifest)
			write(filepath.Join(bin, "curl"), `#!/bin/sh
while [ "$#" -gt 0 ]; do
 case "$1" in
  -o) output=$2; shift 2;;
  --retry) shift 2;;
  -fsSL) shift;;
  *) url=$1; shift;;
 esac
done
case "$url" in
 https://github.com/ericdmoore/call-me-maybe/releases/download/v0.9.9/doorman-linux-amd64)
  cp "$FIXTURE/payload" "$output"
  [ "$DOWNLOAD_FAIL" = false ];;
 https://github.com/ericdmoore/call-me-maybe/releases/download/v0.9.9/checksums.txt)
  [ "$MISSING_MANIFEST" = false ] || exit 22
  cp "$FIXTURE/manifest" "$output";;
 *) exit 22;;
esac
`)
			if tc.tool != "" {
				// Use the host's real digest implementation for successful checks,
				// but expose only the requested tool to the installer.
				realTool, err := exec.LookPath("shasum")
				args := " -a 256"
				if err != nil {
					realTool, err = exec.LookPath("sha256sum")
					args = ""
				}
				if err != nil {
					t.Fatal("tests need a real SHA-256 tool")
				}
				script := "#!/bin/sh\nif [ \"$1\" = -a ]; then shift 2; fi\n"
				switch tc.toolMode {
				case "fail":
					script += "printf '%s\\n' '" + digest + "  payload'\nexit 1\n"
				case "invalid":
					script += "echo invalid\n"
				default:
					script += "PATH=$HOST_PATH exec \"$REAL_HASH\"" + args + " \"$1\"\n"
				}
				write(filepath.Join(bin, tc.tool), script)
				t.Setenv("REAL_HASH", realTool)
			}
			target := filepath.Join(prefix, "doorman")
			write(target, "existing installation\n")
			marker := filepath.Join(root, "executed")
			cmd := exec.Command("bash", "../install.sh", "--version", "0.9.9", "--prefix", prefix, "--force")
			cmd.Env = append(os.Environ(), "PATH="+bin, "HOST_PATH="+os.Getenv("PATH"), "FIXTURE="+root,
				"TMPDIR="+temp, "EXECUTED="+marker, fmt.Sprintf("MISSING_MANIFEST=%t", tc.missingManifest),
				fmt.Sprintf("DOWNLOAD_FAIL=%t", tc.downloadFail))
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.ok {
				t.Fatalf("success=%t: %v\n%s", tc.ok, err, out)
			}
			got, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if tc.ok {
				if string(got) != payload || !strings.Contains(string(out), "checksum verified") {
					t.Fatalf("verified payload not installed: %s", out)
				}
			} else {
				if string(got) != "existing installation\n" {
					t.Fatal("failed verification replaced existing installation")
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("unverified payload was executed")
				}
			}
			entries, err := os.ReadDir(temp)
			if err != nil || len(entries) != 0 {
				t.Fatalf("temporary downloads not cleaned: %v %v", entries, err)
			}
		})
	}
}

package releaseasset

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func TestInstallerReleaseAPIFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture executables use sh")
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile("../../install/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(script), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "TRUSTED_PUBLIC_KEYS=") {
			lines[i] = "TRUSTED_PUBLIC_KEYS='" + base64.StdEncoding.EncodeToString(pub) + "'"
		}
	}
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprintf("corrupt-signature-%v", corrupt), func(t *testing.T) {
			dir := t.TempDir()
			cli := "sparkwing-" + runtime.GOOS + "-" + runtime.GOARCH
			runner := "sparkwing-runner-" + runtime.GOOS + "-" + runtime.GOARCH
			files := map[string][]byte{
				cli:    []byte("#!/bin/sh\n[ \"$1\" != version ] || echo '{\"cli\":{\"installed\":\"v0.1.0\"}}'\n"),
				runner: []byte("#!/bin/sh\necho v0.1.0\n"),
			}
			manifest := ""
			for _, name := range []string{cli, runner} {
				manifest += fmt.Sprintf("%x  %s\n", sha256.Sum256(files[name]), name)
				files[name+".sig"] = ed25519.Sign(key, files[name])
			}
			files["SHA256SUMS"] = []byte(manifest)
			files["SHA256SUMS.sig"] = ed25519.Sign(key, files["SHA256SUMS"])
			if corrupt {
				files[cli+".sig"][0] ^= 1
			}
			names := []string{"SHA256SUMS", "SHA256SUMS.sig", cli, cli + ".sig", runner, runner + ".sig"}
			type apiAsset struct {
				URL      string         `json:"url"`
				ID       int            `json:"id"`
				Name     string         `json:"name"`
				Uploader map[string]any `json:"uploader"`
			}
			var assets []apiAsset
			for i, name := range names {
				assets = append(assets, apiAsset{URL: fmt.Sprintf("https://api.github.com/repos/sparkwing-dev/sparkwing/releases/assets/%d", i+1), ID: i + 1, Name: name, Uploader: map[string]any{"id": 999}})
			}
			var webCalls, apiCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/github/") {
					webCalls.Add(1)
					w.WriteHeader(http.StatusGatewayTimeout)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/tags/v0.1.0") {
					_ = json.NewEncoder(w).Encode(map[string]any{"id": 999, "assets": assets})
					return
				}
				var id int
				if _, err := fmt.Sscanf(r.URL.Path, "/api/repos/sparkwing-dev/sparkwing/releases/assets/%d", &id); err != nil || id < 1 || id > len(names) {
					t.Errorf("unexpected request %s", r.URL)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				apiCalls.Add(1)
				if r.Header.Get("Accept") != "application/octet-stream" {
					t.Error("missing binary Accept header")
				}
				_, _ = w.Write(files[names[id-1]])
			}))
			defer server.Close()
			write := func(name, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			write("install.sh", strings.Join(lines, "\n"))
			write("curl", `#!/bin/bash
args=()
for arg do
 case "$arg" in
  https://github.com/*) arg="$TEST_SERVER/github/${arg#https://github.com/}" ;;
  https://api.github.com/*) arg="$TEST_SERVER/api/${arg#https://api.github.com/}" ;;
 esac
 args+=("$arg")
done
exec /usr/bin/curl "${args[@]}"
`)
			environment := append(os.Environ(), "HOME="+dir, "SPARKWING_HOME="+filepath.Join(dir, "state"), "SPARKWING_RELEASE_BASE_URL=https://github.com/sparkwing-dev/sparkwing/releases/download", "PATH="+dir+":"+os.Getenv("PATH"), "TEST_SERVER="+server.URL)
			prefix := filepath.Join(dir, "prefix")
			command := procenv.ControlCommand(environment, "sh", filepath.Join(dir, "install.sh"), "--version", "v0.1.0", "--runner", "--prefix", prefix)
			output, err := command.CombinedOutput()
			if corrupt {
				if err == nil || !strings.Contains(string(output), "signature over "+cli) {
					t.Fatalf("expected signature refusal: %v\n%s", err, output)
				}
				if _, err := os.Stat(filepath.Join(prefix, "sparkwing")); !os.IsNotExist(err) {
					t.Fatal("installed invalid release")
				}
			} else if err != nil {
				t.Fatalf("installer: %v\n%s", err, output)
			}
			if webCalls.Load() != 6 || apiCalls.Load() != 6 {
				t.Fatalf("web requests=%d API assets=%d", webCalls.Load(), apiCalls.Load())
			}
		})
	}
}

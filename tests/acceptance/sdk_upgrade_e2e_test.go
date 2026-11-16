//go:build linux

package acceptance_test

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/mod/semver"
)

// TestSDKUpgradeE2E walks a first user's upgrade with a real CLI, the published engines and
// the public registries: scaffold, pin the newest listed SDK older than the scaffold's, run,
// `pipeline upgrade`, run again. It needs network access, so it is opt-in.
func TestSDKUpgradeE2E(t *testing.T) {
	cli := os.Getenv("SPARKWING_UPGRADE_E2E_BINARY")
	if cli == "" {
		t.Skip("opt-in: `sparkwing run sdk-upgrade-e2e` sets SPARKWING_UPGRADE_E2E_BINARY to the CLI under test")
	}
	if !filepath.IsAbs(cli) {
		t.Fatal("SPARKWING_UPGRADE_E2E_BINARY must be absolute")
	}
	listed := listedSDKs(t)
	for _, language := range []string{"go", "typescript"} {
		t.Run(language, func(t *testing.T) {
			u := newUpgradeUser(t, cli)
			u.sparkwing("scaffold", "pipeline", "new", "-C", u.project, "--pipeline", "hello", "--template", map[string]string{"go": "minimal", "typescript": "typescript"}[language])
			scaffolded := u.pin(language)
			older := scaffolded
			for _, version := range listed[language] {
				if semver.Compare(version, older) < 0 && (older == scaffolded || semver.Compare(version, older) > 0) {
					older = version
				}
			}
			if older == scaffolded {
				t.Logf("no listed %s SDK is older than the scaffold's %s; upgrade runs as a verified no-op", language, scaffolded)
			}
			u.setPin(language, older)
			u.git("add", "-A")
			u.git("-c", "user.name=e2e", "-c", "user.email=e2e@example.invalid", "commit", "-q", "-m", "pin "+older)
			u.sparkwing("run at "+older, "run", "hello", "-C", u.project, "--sw-allow-unadmitted", "-o", "plain")
			var report struct {
				Before, After     struct{ Version string }
				Applied, Verified bool
			}
			if err := json.Unmarshal(u.sparkwing("upgrade", "pipeline", "upgrade", "-C", u.project, "-o", "json"), &report); err != nil {
				t.Fatalf("upgrade report: %v", err)
			}
			if !report.Verified || !report.Applied {
				t.Fatalf("upgrade from %s did not verify and apply: %+v", older, report)
			}
			if got := u.pin(language); got != "v"+strings.TrimPrefix(report.After.Version, "v") {
				t.Fatalf("upgrade reported %s but the repository pins %s", report.After.Version, got)
			}
			u.sparkwing("run after upgrade to "+report.After.Version, "run", "hello", "-C", u.project, "--sw-allow-unadmitted", "-o", "plain")
		})
	}
}

// listedSDKs reads the SDK entries of the public signed index, the list `pipeline upgrade` selects from.
func listedSDKs(t *testing.T) map[string][]string {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://sparkwing.dev/versions.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("read the published SDK list: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	var index struct {
		SDKs []struct{ Language, Version string } `json:"sdks"`
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("read the published SDK list: %s", response.Status)
	}
	if err := json.NewDecoder(response.Body).Decode(&index); err != nil {
		t.Fatalf("decode the published SDK list: %v", err)
	}
	listed := map[string][]string{}
	for _, sdk := range index.SDKs {
		listed[sdk.Language] = append(listed[sdk.Language], "v"+strings.TrimPrefix(sdk.Version, "v"))
	}
	return listed
}

type upgradeUser struct {
	t       *testing.T
	cli     string
	project string
	env     []string
}

// safety: every Sparkwing home, config and runtime directory is private to the test and the
// session bus is unreachable, so no run touches the developer's store or user services.
func newUpgradeUser(t *testing.T, cli string) *upgradeUser {
	t.Helper()
	caches := map[string]string{}
	for _, name := range []string{"GOMODCACHE", "GOCACHE"} {
		value, err := exec.CommandContext(t.Context(), "go", "env", name).Output()
		if err != nil {
			t.Fatalf("go env %s: %v", name, err)
		}
		caches[name] = strings.TrimSpace(string(value))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	u := &upgradeUser{t: t, cli: cli, project: filepath.Join(root, "project")}
	private := map[string]string{"HOME": "home", "SPARKWING_HOME": "sparkwing", "XDG_CONFIG_HOME": "config", "XDG_DATA_HOME": "data", "XDG_STATE_HOME": "state", "XDG_CACHE_HOME": "cache", "XDG_RUNTIME_DIR": "run"}
	for _, setting := range os.Environ() {
		name, _, _ := strings.Cut(setting, "=")
		if _, ok := private[name]; ok || strings.HasPrefix(name, "SPARKWING_") || name == "DBUS_SESSION_BUS_ADDRESS" || name == "GOWORK" || name == "GOFLAGS" {
			continue
		}
		u.env = append(u.env, setting)
	}
	for name, dir := range private {
		path := filepath.Join(root, dir)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		u.env = append(u.env, name+"="+path)
	}
	key := filepath.Join(root, "sparkwing", "secrets.key")
	if err := os.WriteFile(key, secretKey(t), 0o600); err != nil {
		t.Fatal(err)
	}
	u.env = append(u.env, "SPARKWING_SECRETS_KEY_FILE="+key, "DBUS_SESSION_BUS_ADDRESS=unix:path="+filepath.Join(root, "no-bus"),
		"GOMODCACHE="+caches["GOMODCACHE"], "GOCACHE="+caches["GOCACHE"], "npm_config_cache="+filepath.Join(home, ".npm"))
	if err := os.Mkdir(u.project, 0o700); err != nil {
		t.Fatal(err)
	}
	u.git("init", "-q")
	return u
}

func secretKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

func (u *upgradeUser) sparkwing(step string, args ...string) []byte {
	u.t.Helper()
	command := exec.CommandContext(u.t.Context(), u.cli, args...)
	command.Env = u.env
	var stderr strings.Builder
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		u.t.Fatalf("%s: sparkwing %s: %v\nstdout: %s\nstderr: %s", step, strings.Join(args, " "), err, output, stderr.String())
	}
	u.t.Logf("%s: ok", step)
	return output
}

func (u *upgradeUser) tool(dir, name string, args ...string) {
	u.t.Helper()
	command := exec.CommandContext(u.t.Context(), name, args...)
	command.Dir, command.Env = dir, append(u.env, "GOWORK=off", "GOFLAGS=-mod=mod")
	if output, err := command.CombinedOutput(); err != nil {
		u.t.Fatalf("%s %s: %v: %s", name, strings.Join(args, " "), err, output)
	}
}

func (u *upgradeUser) git(args ...string) { u.t.Helper(); u.tool(u.project, "git", args...) }

var goSDKRequire = regexp.MustCompile(`(?m)^\s*(?:require\s+)?github\.com/sparkwing-dev/sparkwing/sparkwing\s+(v\S+)`)

// pin reads the exact SDK the scaffold or upgrade wrote, as a v-prefixed semantic version.
func (u *upgradeUser) pin(language string) string {
	u.t.Helper()
	pipeline := filepath.Join(u.project, ".sparkwing")
	if language == "go" {
		body, err := os.ReadFile(filepath.Join(pipeline, "go.mod"))
		if err != nil {
			u.t.Fatal(err)
		}
		match := goSDKRequire.FindSubmatch(body)
		if match == nil {
			u.t.Fatalf("go.mod has no SDK requirement:\n%s", body)
		}
		return string(match[1])
	}
	body, err := os.ReadFile(filepath.Join(pipeline, "package.json"))
	if err != nil {
		u.t.Fatal(err)
	}
	var manifest struct{ Dependencies map[string]string }
	if err := json.Unmarshal(body, &manifest); err != nil {
		u.t.Fatal(err)
	}
	return "v" + manifest.Dependencies["@sparkwing/sdk"]
}

func (u *upgradeUser) setPin(language, version string) {
	u.t.Helper()
	pipeline := filepath.Join(u.project, ".sparkwing")
	if language == "go" {
		u.tool(pipeline, "go", "get", "github.com/sparkwing-dev/sparkwing/sparkwing@"+version)
		u.tool(pipeline, "go", "mod", "tidy")
		return
	}
	path := filepath.Join(pipeline, "package.json")
	body, err := os.ReadFile(path)
	if err != nil {
		u.t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(body, &manifest); err != nil {
		u.t.Fatal(err)
	}
	manifest["dependencies"].(map[string]any)["@sparkwing/sdk"] = strings.TrimPrefix(version, "v")
	body, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		u.t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		u.t.Fatal(err)
	}
	u.tool(pipeline, "npm", "install", "--package-lock-only", "--ignore-scripts", "--no-audit", "--no-fund")
}

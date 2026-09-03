package fleet

import (
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), Filename)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadRequiresFixedExplicitNetworkAndKnownFields(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"zero port", "listen: 127.0.0.1:0\npublic_url: http://127.0.0.1:0\n", "fixed host:port"},
		{"unknown", "listen: 127.0.0.1:7443\npublic_url: http://127.0.0.1:7443\nsecret_token: nope\n", "field secret_token not found"},
		{"second document", "listen: 127.0.0.1:7443\npublic_url: http://127.0.0.1:7443\n---\nallow_tailnet_http: true\n", "multiple YAML documents"},
		{"proxy exposes plaintext listener", "listen: 0.0.0.0:7443\npublic_url: https://desk.example\n", "not a wildcard"},
		{"loopback advertisement hides wildcard listener", "listen: :7443\npublic_url: http://127.0.0.1:7443\n", "not a wildcard"},
		{"hostname listener", "listen: localhost:7443\npublic_url: http://localhost:7443\n", "literal local IP"},
		{"magic dns is not transport proof", "listen: desk.tailnet.ts.net:7443\npublic_url: http://desk.tailnet.ts.net:7443\nallow_tailnet_http: true\n", "literal"},
		{"plaintext escape on loopback", "listen: 127.0.0.1:7443\npublic_url: http://127.0.0.1:7443\nallow_tailnet_http: true\n", "applies only"},
		{"plaintext escape on https", "listen: 127.0.0.1:7443\npublic_url: https://desk.example\nallow_tailnet_http: true\n", "applies only"},
		{"unsafe name", "listen: 127.0.0.1:7443\npublic_url: http://127.0.0.1:7443\nexecutors: [{name: 'desk\\nspoof', location: local}]\n", "name must be unique"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.body), nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLoadPreservesExplicitZeroPriorityCeiling(t *testing.T) {
	path := writeConfig(t, `listen: 127.0.0.1:7443
public_url: http://127.0.0.1:7443
executors:
  - name: constrained
    location: local
    base_priority: 0
    priority_ceiling: 0
`)
	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Executors[0].PriorityCeiling; got != 0 {
		t.Fatalf("priority ceiling = %d, want explicit zero", got)
	}
}

func TestLoadRejectsCapabilitiesThatSpoofMachineOrPlacementFacts(t *testing.T) {
	for _, capability := range []string{"os=linux", "ARCH=arm64", "environment=wsl", "location=cloud"} {
		path := writeConfig(t, "listen: 127.0.0.1:7443\npublic_url: http://127.0.0.1:7443\nlocal:\n  capabilities: ["+capability+"]\n")
		if _, err := Load(path, nil); err == nil || !strings.Contains(err.Error(), "reserved machine or placement key") {
			t.Fatalf("capability %q error = %v", capability, err)
		}
	}
}

func TestLoadDefaultsAndValidatesLocalContribution(t *testing.T) {
	path := writeConfig(t, "listen: 127.0.0.1:7443\npublic_url: http://127.0.0.1:7443\nlocal:\n  local_reserve: 1,2gb\n")
	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Local.Contribution != "50%,50%" || cfg.Local.LocalReserve != "1,2gb" || cfg.Local.MaxConcurrent != 1 {
		t.Fatalf("local defaults = %+v", cfg.Local)
	}
	bad := writeConfig(t, "listen: 127.0.0.1:7443\npublic_url: http://127.0.0.1:7443\nlocal:\n  contribution: nope\n")
	if _, err := Load(bad, nil); err == nil || !strings.Contains(err.Error(), "local.contribution") {
		t.Fatalf("invalid contribution error = %v", err)
	}
	for _, field := range []string{"contribution: enforce", "local_reserve: ignore-external"} {
		bad = writeConfig(t, "listen: 127.0.0.1:7443\npublic_url: http://127.0.0.1:7443\nlocal:\n  "+field+"\n")
		if _, err := Load(bad, nil); err == nil || !strings.Contains(err.Error(), "local.") {
			t.Fatalf("invalid %s error = %v", field, err)
		}
	}
}

func TestLoadAllowsOnlyVerifiedLiteralTailscaleHTTP(t *testing.T) {
	path := writeConfig(t, "listen: 100.64.1.2:7443\npublic_url: http://100.64.1.2:7443\nallow_tailnet_http: true\n")
	lookup := func() ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr("100.64.1.2")}, nil }
	cfg, err := Load(path, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicURL != "http://100.64.1.2:7443" {
		t.Fatalf("public URL = %q", cfg.PublicURL)
	}
	otherMachine := func() ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr("100.64.1.3")}, nil }
	if _, err := Load(path, otherMachine); err == nil || !strings.Contains(err.Error(), "is not a local Tailscale IP") {
		t.Fatalf("nonlocal tailnet address error = %v", err)
	}
}

func TestLoadRejectsReachableConfigSymlinkOrBroadMode(t *testing.T) {
	target := writeConfig(t, "listen: 127.0.0.1:7443\npublic_url: http://127.0.0.1:7443\n")
	link := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(link, nil); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink error = %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(target, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(target, nil); err == nil || !strings.Contains(err.Error(), "owner-only") {
			t.Fatalf("mode error = %v", err)
		}
	}
}

func TestAppendExecutorPublishesOnlyPolicyAndPreservesConfig(t *testing.T) {
	path := writeConfig(t, "listen: 127.0.0.1:7443\npublic_url: http://127.0.0.1:7443\nlocal:\n  name: laptop\n  contribution: 25%,25%\n")
	cfg, err := AppendExecutor(path, Executor{
		Name: "desk", Location: "local",
		BasePriority: 50, PriorityCeiling: 80, MaxConcurrent: 2,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Local.Name != "laptop" || cfg.Local.Contribution != "25%,25%" || len(cfg.Executors) != 1 {
		t.Fatalf("updated config = %+v", cfg)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "secret") || strings.Contains(string(body), "token_prefix") || strings.Contains(string(body), "swr_") {
		t.Fatalf("persisted config = %s", body)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, %v", info.Mode().Perm(), err)
		}
	}
}

func TestCreateWritesOwnerOnlyConfigAndNeverReplacesIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", Filename)
	cfg := Config{
		Listen: "127.0.0.1:7443", PublicURL: "http://127.0.0.1:7443",
		Local: Local{MaxConcurrent: 1, Contribution: "50%,50%"},
	}
	if err := Create(path, cfg, nil); err != nil {
		t.Fatal(err)
	}
	if err := Create(path, cfg, nil); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second Create error = %v", err)
	}
	loaded, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Listen != cfg.Listen || loaded.Local.Contribution != "50%,50%" {
		t.Fatalf("loaded config = %+v", loaded)
	}
}

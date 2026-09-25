package main

import (
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fleet"
)

func TestFleetPublicSurfaceStaysSmallAndCredentialFree(t *testing.T) {
	if !slices.Equal(cmdFleet.SubcommandOrder, []string{"init"}) {
		t.Fatalf("fleet subcommands = %v", cmdFleet.SubcommandOrder)
	}
	wantInit := []string{"--allow-tailnet-http", "--listen", "--public-url", "--tailnet"}
	gotInit := flagNames(cmdFleetInit.Flags)
	slices.Sort(gotInit)
	if !slices.Equal(gotInit, wantInit) {
		t.Fatalf("fleet init flags = %v, want %v", gotInit, wantInit)
	}
	for _, help := range []string{cmdFleet.Description, cmdFleetInit.Description} {
		for _, forbidden := range []string{"--principal", "--kind", "gateway", "token-prefix", "authority_id"} {
			if strings.Contains(help, forbidden) {
				t.Errorf("fleet help exposes %q: %s", forbidden, help)
			}
		}
	}
}

func TestDirectTailnetFleetConfigIsExplicitAndUnambiguous(t *testing.T) {
	cfg, err := directTailnetFleetConfig([]netip.Addr{
		netip.MustParseAddr("fd7a:115c:a1e0::1"), netip.MustParseAddr("100.64.1.2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "100.64.1.2:4346" || cfg.PublicURL != "http://100.64.1.2:4346" || !cfg.AllowTailnetHTTP {
		t.Fatalf("direct tailnet config = %+v", cfg)
	}
	if _, err := directTailnetFleetConfig([]netip.Addr{netip.MustParseAddr("100.64.1.2"), netip.MustParseAddr("100.64.1.3")}); err == nil {
		t.Fatal("ambiguous Tailscale IPv4 addresses were accepted")
	}
}

func TestFleetInitCreatesCredentialFreePolicyAndRefusesReplacement(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("# this machine\nprofiles:\n  laptop: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_CONFIG", configPath)
	output := captureStdout(t, func() {
		if err := runFleetInit([]string{
			"--listen", "127.0.0.1:4346", "--public-url", "https://fleet.example.test",
		}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(output, configPath) {
		t.Fatalf("fleet init output = %q", output)
	}
	cfg, err := fleet.Load(configPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:4346" || cfg.PublicURL != "https://fleet.example.test" || len(cfg.Executors) != 0 {
		t.Fatalf("initialized fleet config = %+v", cfg)
	}
	body := string(mustReadFleetFile(t, configPath))
	if !strings.Contains(body, "# this machine") || !strings.Contains(body, "laptop: {}") {
		t.Fatalf("fleet init lost the file's other content: %s", body)
	}
	for _, forbidden := range []string{"token", "prefix", "credential", "principal", "authority_id"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Fatalf("initialized fleet config exposes %q: %s", forbidden, body)
		}
	}
	if err := runFleetInit([]string{
		"--listen", "127.0.0.1:4346", "--public-url", "https://other.example.test",
	}); err == nil || !strings.Contains(err.Error(), "already has a fleet section") {
		t.Fatalf("replacement fleet init error = %v", err)
	}
}

func mustReadFleetFile(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

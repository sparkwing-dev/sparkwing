package fleet

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"testing"
)

func TestLocalTailscaleCandidatesIncludesNativeClientFromWSL(t *testing.T) {
	t.Setenv("WSL_INTEROP", "/run/WSL/1_interop")
	candidates := localTailscaleCandidates()
	if !slices.Contains(candidates, "/mnt/c/Program Files/Tailscale/tailscale.exe") ||
		!slices.Contains(candidates, "/mnt/c/Progra~1/Tailscale/tailscale.exe") {
		t.Fatalf("WSL Tailscale candidates = %q", candidates)
	}
}

func TestProbeLocalTailscaleIPsUsesTheNativeWindowsClientFromWSLWithoutDiscovery(t *testing.T) {
	const nativeClient = "/mnt/c/Progra~1/Tailscale/tailscale.exe"
	var commands [][]string
	ips, err := probeLocalTailscaleIPs(context.Background(), []string{"tailscale", nativeClient, "unused"},
		func(candidate string) (string, error) {
			if candidate != nativeClient {
				return "", errors.New("not found")
			}
			return candidate, nil
		},
		func(_ context.Context, path string, args ...string) ([]byte, error) {
			commands = append(commands, append([]string{path}, args...))
			switch args[1] {
			case "-4":
				return []byte("100.67.211.34\n"), nil
			case "-6":
				return []byte("fd7a:115c:a1e0::1\n"), nil
			default:
				return nil, errors.New("unexpected Tailscale command")
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	wantIPs := []netip.Addr{netip.MustParseAddr("100.67.211.34"), netip.MustParseAddr("fd7a:115c:a1e0::1")}
	if !slices.Equal(ips, wantIPs) {
		t.Fatalf("local Tailscale IPs = %v, want %v", ips, wantIPs)
	}
	wantCommands := [][]string{{nativeClient, "ip", "-4"}, {nativeClient, "ip", "-6"}}
	if len(commands) != len(wantCommands) {
		t.Fatalf("Tailscale commands = %q, want %q", commands, wantCommands)
	}
	for i := range commands {
		if !slices.Equal(commands[i], wantCommands[i]) {
			t.Fatalf("Tailscale command %d = %q, want %q", i, commands[i], wantCommands[i])
		}
	}
}

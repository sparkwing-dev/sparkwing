package fleet

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"time"
)

// LocalTailscaleIPs asks the local Tailscale client only for this machine's
// addresses. It never scans peers or treats DNS names as proof of transport.
func LocalTailscaleIPs() ([]netip.Addr, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return probeLocalTailscaleIPs(ctx, localTailscaleCandidates(), exec.LookPath,
		func(ctx context.Context, path string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, path, args...).Output()
		})
}

func probeLocalTailscaleIPs(
	ctx context.Context,
	candidates []string,
	lookPath func(string) (string, error),
	output func(context.Context, string, ...string) ([]byte, error),
) ([]netip.Addr, error) {
	var failures []error
	var out []netip.Addr
	for _, candidate := range candidates {
		path, err := lookPath(candidate)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		for _, family := range []string{"-4", "-6"} {
			body, err := output(ctx, path, "ip", family)
			if err != nil {
				failures = append(failures, fmt.Errorf("%s ip %s: %w", candidate, family, err))
				continue
			}
			for _, field := range strings.Fields(string(body)) {
				if ip, err := netip.ParseAddr(field); err == nil {
					out = append(out, ip.Unmap())
				}
			}
		}
		if len(out) > 0 {
			return out, nil
		}
	}
	if len(failures) == 0 {
		return nil, errors.New("Tailscale returned no local IPs")
	}
	return nil, errors.Join(failures...)
}

func localTailscaleCandidates() []string {
	candidates := []string{"tailscale", "tailscale.exe"}
	if os.Getenv("WSL_INTEROP") != "" || os.Getenv("WSL_DISTRO_NAME") != "" {
		candidates = append(candidates,
			"/mnt/c/Program Files/Tailscale/tailscale.exe",
			"/mnt/c/Progra~1/Tailscale/tailscale.exe",
		)
	}
	return candidates
}

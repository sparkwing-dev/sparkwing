package main

import (
	"strings"
	"testing"
)

func TestTrustedProxyCIDRsRejectInvalidConfiguration(t *testing.T) {
	for _, value := range []string{"not-a-network", "10.0.0.1", "10.0.0.0/8,,192.0.2.0/24", "::ffff:10.0.0.0/104"} {
		err := run([]string{"--trusted-proxy-cidrs=" + value})
		if err == nil || !strings.Contains(err.Error(), "invalid --trusted-proxy-cidrs") {
			t.Fatalf("CIDRs %q: %v", value, err)
		}
	}
}

func TestTrustedProxyCIDRsAcceptValidConfiguration(t *testing.T) {
	for _, value := range []string{"", "10.0.0.0/8,2001:db8:ffff::/48", "192.0.2.17/24"} {
		err := run([]string{"--trusted-proxy-cidrs=" + value, "--credentials-dir=" + t.TempDir() + "/missing"})
		if err == nil || !strings.Contains(err.Error(), "--credentials-dir") {
			t.Fatalf("valid CIDRs %q did not reach credential validation: %v", value, err)
		}
	}
}

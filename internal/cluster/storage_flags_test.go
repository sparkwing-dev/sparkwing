package cluster

import (
	"strings"
	"testing"
)

func TestLaunchRefusesRemovedCacheFlags(t *testing.T) {
	for _, name := range []string{"cache", "gitcache", "dependency-proxy"} {
		t.Run(name, func(t *testing.T) {
			err := runLaunchCLI([]string{"--" + name + "=http://cache"})
			if err == nil || !strings.Contains(err.Error(), "unknown flag: --"+name) {
				t.Fatalf("removed flag %s: %v", name, err)
			}
		})
	}
}

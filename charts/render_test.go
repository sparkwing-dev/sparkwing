// Package charts holds the deployment charts. Its only Go code is this
// rendering test: the chart is a supported surface, and the arguments
// it hands the web pod are the difference between a dashboard that can
// see the cluster's cache and one that cannot.
package charts

import (
	"os/exec"
	"strings"
	"testing"
)

// helmTemplate renders one template out of sparkwing-full with the
// given --set overrides. Rendering needs the helm binary, so these
// tests only run where it is installed and skip everywhere else.
func helmTemplate(t *testing.T, release string, sets ...string) string {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not installed; chart rendering not exercised")
	}
	args := []string{
		"template", release, "./sparkwing-full",
		"--show-only", "templates/web-deployment.yaml",
	}
	for _, s := range sets {
		args = append(args, "--set", s)
	}
	out, err := exec.Command(helm, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// webArgs pulls the container's args list out of the rendered
// Deployment, so an assertion reads flags rather than YAML.
func webArgs(t *testing.T, rendered string) []string {
	t.Helper()
	var args []string
	inArgs := false
	for _, line := range strings.Split(rendered, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "args:":
			inArgs = true
		case inArgs && strings.HasPrefix(trimmed, "- "):
			args = append(args, strings.Trim(strings.TrimPrefix(trimmed, "- "), `"`))
		case inArgs && trimmed != "":
			inArgs = false
		}
	}
	if len(args) == 0 {
		t.Fatalf("no container args in the rendered Deployment:\n%s", rendered)
	}
	return args
}

func hasFlag(args []string, prefix string) (string, bool) {
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			return a, true
		}
	}
	return "", false
}

// A default install bundles the runner, so the dashboard is pointed at
// the cache that install created. Nothing else in the chart reads the
// cache; without this the services panel omits the one component that
// reports a stalled fetch loop, and runner builds degrade to cold
// clones with no lamp anywhere.
func TestWebIsPointedAtTheBundledCache(t *testing.T) {
	args := webArgs(t, helmTemplate(t, "sparkwing"))
	got, ok := hasFlag(args, "--cache=")
	if !ok {
		t.Fatalf("no --cache flag in %v", args)
	}
	const want = "--cache=http://sparkwing-sparkwing-runner-bundle-cache.default.svc.cluster.local"
	if got != want {
		t.Errorf("cache flag = %q, want %q", got, want)
	}
	// The logs URL is computed by the same sub-chart-name helper; pin
	// it so a change there cannot quietly move the logs backend.
	if got, _ := hasFlag(args, "--logs="); got !=
		"--logs=http://sparkwing-sparkwing-runner-bundle-logs.default.svc.cluster.local" {
		t.Errorf("logs flag = %q, want the bundled logs Service", got)
	}
}

func TestWebCacheURLOverrideWins(t *testing.T) {
	args := webArgs(t, helmTemplate(t, "sparkwing", "web.cache.url=http://cache.elsewhere:8090"))
	if got, _ := hasFlag(args, "--cache="); got != "--cache=http://cache.elsewhere:8090" {
		t.Errorf("cache flag = %q, want the explicit override", got)
	}
}

// An operator who runs their own git mirror disables the bundled cache.
// The web pod must then start with no --cache at all rather than a URL
// pointing at a Service the chart did not create: an absent cache is
// absent from the panel, not a red lamp for something nobody deployed.
func TestWebHasNoCacheFlagWhenNoCacheIsDeployed(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  string
	}{
		{name: "cache component disabled", set: "sparkwing-runner-bundle.cache.enabled=false"},
		{name: "whole bundle disabled", set: "sparkwing-runner-bundle.enabled=false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := webArgs(t, helmTemplate(t, "sparkwing", tc.set))
			if got, ok := hasFlag(args, "--cache="); ok {
				t.Errorf("rendered %q with no cache deployed", got)
			}
		})
	}
}

// A release named after the sub-chart collapses the doubled name, and
// the cache Service name has to follow it or the dashboard probes a
// host that does not resolve.
func TestWebCacheURLFollowsTheSubChartNaming(t *testing.T) {
	args := webArgs(t, helmTemplate(t, "sparkwing-runner-bundle"))
	if got, _ := hasFlag(args, "--cache="); got !=
		"--cache=http://sparkwing-runner-bundle-cache.default.svc.cluster.local" {
		t.Errorf("cache flag = %q, want the collapsed release name", got)
	}
}

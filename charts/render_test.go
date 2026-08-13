// Package charts holds the deployment charts. Its only Go code is this
// rendering test: the chart is a supported surface, and the arguments
// it hands the web pod are the difference between a dashboard that can
// see the cluster's cache and one that cannot.
package charts

import (
	"os/exec"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// helmRender renders one template out of one chart with the given
// --set overrides. Rendering needs the helm binary, so these tests
// only run where it is installed and skip everywhere else.
func helmRender(t *testing.T, chart, showOnly, release string, sets ...string) string {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not installed; chart rendering not exercised")
	}
	args := []string{"template", release, chart, "--show-only", showOnly}
	for _, s := range sets {
		args = append(args, "--set", s)
	}
	out, err := exec.Command(helm, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// helmTemplate renders sparkwing-full's web Deployment.
func helmTemplate(t *testing.T, release string, sets ...string) string {
	t.Helper()
	return helmRender(t, "./sparkwing-full", "templates/web-deployment.yaml", release, sets...)
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

// renderedEnvVar / renderedContainer / renderedDeployment are the
// slice of a Deployment these tests read.
type renderedEnvVar struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

type renderedContainer struct {
	Args []string         `yaml:"args"`
	Env  []renderedEnvVar `yaml:"env"`
}

type renderedDeployment struct {
	Kind string `yaml:"kind"`
	Spec struct {
		Template struct {
			Spec struct {
				Containers []renderedContainer `yaml:"containers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

// runnerContainer decodes the rendered runner Deployment down to the
// one container. Decoding the YAML (rather than grepping it) is what
// lets a test assert that an env name appears exactly once: duplicate
// names are a K8s validation error, so "the user's value wins" has to
// mean "the default was never emitted".
func runnerContainer(t *testing.T, rendered string) renderedContainer {
	t.Helper()
	var doc renderedDeployment
	dec := yaml.NewDecoder(strings.NewReader(rendered))
	for {
		if err := dec.Decode(&doc); err != nil {
			t.Fatalf("no Deployment in the rendered output (%v):\n%s", err, rendered)
		}
		if doc.Kind == "Deployment" {
			break
		}
	}
	containers := doc.Spec.Template.Spec.Containers
	if len(containers) != 1 {
		t.Fatalf("containers = %d, want 1:\n%s", len(containers), rendered)
	}
	return containers[0]
}

func runnerEnv(t *testing.T, rendered string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, e := range runnerContainer(t, rendered).Env {
		if _, dup := out[e.Name]; dup {
			t.Errorf("env %q appears twice; K8s rejects duplicate env names", e.Name)
		}
		out[e.Name] = e.Value
	}
	return out
}

func renderRunner(t *testing.T, sets ...string) string {
	t.Helper()
	return helmRender(t, "./sparkwing-runner-bundle", "templates/runner-deployment.yaml", "sparkwing", sets...)
}

// A default install pays for every dependency fetch twice: once in
// egress, once in wall time. With the cache deployed, the runner's
// package managers point at its pull-through proxy, and the values
// carry the fallbacks that keep a rolling cache pod from failing
// builds.
func TestRunnerPackageManagersUseTheBundledDependencyProxy(t *testing.T) {
	env := runnerEnv(t, renderRunner(t))
	const host = "sparkwing-sparkwing-runner-bundle-cache.default.svc.cluster.local"
	for key, want := range map[string]string{
		"GOPROXY":             "http://" + host + "/proxy/golang|https://proxy.golang.org,direct",
		"npm_config_registry": "http://" + host + "/proxy/npm",
		"PIP_INDEX_URL":       "http://" + host + "/proxy/pypi/simple/",
		"PIP_TRUSTED_HOST":    host,
	} {
		if got := env[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// An operator with their own mirror overrides one ecosystem without
// giving up the others. The default must not be emitted alongside the
// override, or the Deployment is rejected outright.
func TestRunnerExtraEnvOverridesADependencyProxyDefault(t *testing.T) {
	env := runnerEnv(t, renderRunner(t,
		"runner.extraEnv[0].name=GOPROXY",
		"runner.extraEnv[0].value=https://goproxy.internal"))
	if got := env["GOPROXY"]; got != "https://goproxy.internal" {
		t.Errorf("GOPROXY = %q, want the extraEnv override", got)
	}
	if env["npm_config_registry"] == "" {
		t.Error("overriding GOPROXY should not drop the other proxy defaults")
	}
}

// Opting out has to reach the pods the runner spawns too: those derive
// the proxy from --gitcache, so the flag carries the decision.
func TestRunnerDependencyProxyOptOut(t *testing.T) {
	rendered := renderRunner(t, "cache.dependencyProxy.enabled=false")
	env := runnerEnv(t, rendered)
	for _, key := range []string{"GOPROXY", "npm_config_registry", "PIP_INDEX_URL", "PIP_TRUSTED_HOST"} {
		if _, ok := env[key]; ok {
			t.Errorf("%s set with cache.dependencyProxy.enabled=false", key)
		}
	}
	if args := runnerContainer(t, rendered).Args; !containsArg(args, "--dependency-proxy=off") {
		t.Errorf("runner args = %v, want --dependency-proxy=off", args)
	}
}

// No cache deployed, no proxy to point at: the runner keeps its
// upstream defaults rather than a URL for a Service the chart did not
// create.
func TestRunnerHasNoDependencyProxyWhenNoCacheIsDeployed(t *testing.T) {
	rendered := renderRunner(t, "cache.enabled=false")
	env := runnerEnv(t, rendered)
	for _, key := range []string{"GOPROXY", "npm_config_registry", "PIP_INDEX_URL", "PIP_TRUSTED_HOST"} {
		if _, ok := env[key]; ok {
			t.Errorf("%s set with cache.enabled=false", key)
		}
	}
	for _, arg := range runnerContainer(t, rendered).Args {
		if strings.HasPrefix(arg, "--dependency-proxy") {
			t.Errorf("runner args carry %q with no cache deployed", arg)
		}
	}
}

// sparkwing-full vendors the runner bundle as a packaged .tgz under
// charts/, so a sub-chart edit reaches full-self-host installs only
// after the dependency is re-packaged. Assert the wiring through the
// parent chart as well: a pass means the vendored copy is current.
func TestFullChartCarriesTheDependencyProxyWiring(t *testing.T) {
	env := runnerEnv(t, helmRender(t, "./sparkwing-full",
		"charts/sparkwing-runner-bundle/templates/runner-deployment.yaml", "sparkwing"))
	const host = "sparkwing-sparkwing-runner-bundle-cache.default.svc.cluster.local"
	if got := env["GOPROXY"]; got != "http://"+host+"/proxy/golang|https://proxy.golang.org,direct" {
		t.Errorf("GOPROXY = %q; the vendored sub-chart may be stale. "+
			"Fix: helm dep up ./charts/sparkwing-full", got)
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// The controller must announce the logs service, because it serves no
// /api/v1/logs route itself. A client with no logs: surface of its own
// -- a laptop running `sparkwing run --profile prod` -- discovers the
// URL from the controller and posts there; without the announcement it
// falls back to the controller's own URL and every append 404s.
//
// The in-cluster runner does not depend on this: its Deployment passes
// --logs directly. This flag is for everything reaching in from outside.
func TestControllerAnnouncesTheBundledLogsService(t *testing.T) {
	rendered := helmRender(t, "./sparkwing-full", "templates/controller-deployment.yaml", "sparkwing")
	args := webArgs(t, rendered)
	got, ok := hasFlag(args, "--logs-url=")
	if !ok {
		t.Fatalf("no --logs-url flag in %v; a client with no logs surface would post to the controller, which serves none", args)
	}
	const want = "--logs-url=http://sparkwing-sparkwing-runner-bundle-logs.default.svc.cluster.local"
	if got != want {
		t.Errorf("logs-url flag = %q, want the bundled logs Service", got)
	}
}

// With no logs component deployed there is nothing to announce, and the
// flag must be absent rather than empty -- an empty announcement would
// advertise a URL that resolves nowhere.
func TestControllerAnnouncesNoLogsServiceWhenNoneIsDeployed(t *testing.T) {
	rendered := helmRender(t, "./sparkwing-full", "templates/controller-deployment.yaml", "sparkwing",
		"sparkwing-runner-bundle.logs.enabled=false")
	args := webArgs(t, rendered)
	if got, ok := hasFlag(args, "--logs-url="); ok {
		t.Errorf("got %q, want no flag when no logs service is deployed", got)
	}
}

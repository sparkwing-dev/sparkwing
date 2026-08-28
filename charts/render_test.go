// Package charts holds the deployment charts. Its only Go code is this
// rendering test: the chart is a supported surface, and the arguments
// it hands the web pod are the difference between a dashboard that can
// see the cluster's cache and one that cannot.
package charts

import (
	"io"
	"os/exec"
	"reflect"
	"regexp"
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

func helmRenderInNamespace(t *testing.T, chart, showOnly, release, namespace string, sets ...string) string {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not installed; chart rendering not exercised")
	}
	args := []string{"template", release, chart, "--namespace", namespace, "--show-only", showOnly}
	for _, s := range sets {
		args = append(args, "--set", s)
	}
	out, err := exec.Command(helm, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func helmRenderAll(t *testing.T, chart, release, namespace string, sets ...string) string {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not installed; chart rendering not exercised")
	}
	args := []string{"template", release, chart, "--namespace", namespace}
	for _, s := range sets {
		args = append(args, "--set", s)
	}
	out, err := exec.Command(helm, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func helmRenderError(t *testing.T, chart, release string, sets ...string) string {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not installed; chart rendering not exercised")
	}
	args := []string{"template", release, chart}
	for _, s := range sets {
		args = append(args, "--set", s)
	}
	out, err := exec.Command(helm, args...).CombinedOutput()
	if err == nil {
		t.Fatalf("helm %s succeeded, want an actionable render failure", strings.Join(args, " "))
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
		sets []string
	}{
		{
			name: "cache component disabled for a node-only pool",
			sets: []string{
				"sparkwing-runner-bundle.cache.enabled=false",
				"sparkwing-runner-bundle.runner.alsoClaimTriggers=false",
			},
		},
		{name: "whole bundle disabled", sets: []string{"sparkwing-runner-bundle.enabled=false"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := webArgs(t, helmTemplate(t, "sparkwing", tc.sets...))
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

type renderedCapabilities struct {
	Add  []string `yaml:"add"`
	Drop []string `yaml:"drop"`
}

type renderedSecurityContext struct {
	RunAsNonRoot             *bool                `yaml:"runAsNonRoot"`
	RunAsUser                *int64               `yaml:"runAsUser"`
	RunAsGroup               *int64               `yaml:"runAsGroup"`
	AllowPrivilegeEscalation *bool                `yaml:"allowPrivilegeEscalation"`
	ReadOnlyRootFilesystem   *bool                `yaml:"readOnlyRootFilesystem"`
	Capabilities             renderedCapabilities `yaml:"capabilities"`
}

type renderedVolumeMount struct {
	Name      string `yaml:"name"`
	MountPath string `yaml:"mountPath"`
}

type renderedContainer struct {
	Name            string                  `yaml:"name"`
	Image           string                  `yaml:"image"`
	Command         []string                `yaml:"command"`
	Args            []string                `yaml:"args"`
	Env             []renderedEnvVar        `yaml:"env"`
	SecurityContext renderedSecurityContext `yaml:"securityContext"`
	VolumeMounts    []renderedVolumeMount   `yaml:"volumeMounts"`
}

type renderedDeployment struct {
	Kind string `yaml:"kind"`
	Spec struct {
		Template struct {
			Spec struct {
				SecurityContext renderedSecurityContext `yaml:"securityContext"`
				InitContainers  []renderedContainer     `yaml:"initContainers"`
				Containers      []renderedContainer     `yaml:"containers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

type renderedResource struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name   string            `yaml:"name"`
		Labels map[string]string `yaml:"labels"`
	} `yaml:"metadata"`
	Spec struct {
		Template struct {
			Spec struct {
				InitContainers []renderedContainer `yaml:"initContainers"`
				Containers     []renderedContainer `yaml:"containers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

func renderedResources(t *testing.T, rendered string) []renderedResource {
	t.Helper()
	dec := yaml.NewDecoder(strings.NewReader(rendered))
	var resources []renderedResource
	for {
		var resource renderedResource
		err := dec.Decode(&resource)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decode rendered resources: %v\n%s", err, rendered)
		}
		if resource.Kind != "" {
			resources = append(resources, resource)
		}
	}
	return resources
}

var dnsLabel = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$`)

func assertValidUniqueResourceNames(t *testing.T, resources []renderedResource) {
	t.Helper()
	seen := map[string]bool{}
	for _, resource := range resources {
		name := resource.Metadata.Name
		if len(name) > 63 || !dnsLabel.MatchString(name) {
			t.Errorf("%s name %q is not a valid DNS label", resource.Kind, name)
		}
		key := resource.Kind + "/" + name
		if seen[key] {
			t.Errorf("rendered duplicate %s", key)
		}
		seen[key] = true
	}
}

func componentResource(t *testing.T, resources []renderedResource, kind, component string) renderedResource {
	t.Helper()
	for _, resource := range resources {
		if resource.Kind == kind && resource.Metadata.Labels["app.kubernetes.io/component"] == component {
			return resource
		}
	}
	t.Fatalf("no %s with component=%s", kind, component)
	return renderedResource{}
}

func resourceContainer(t *testing.T, resource renderedResource) renderedContainer {
	t.Helper()
	containers := resource.Spec.Template.Spec.Containers
	if len(containers) != 1 {
		t.Fatalf("%s/%s containers = %d, want 1", resource.Kind, resource.Metadata.Name, len(containers))
	}
	return containers[0]
}

// runnerContainer decodes the rendered runner Deployment down to the
// one container. Decoding the YAML (rather than grepping it) is what
// lets a test assert that an env name appears exactly once: duplicate
// names are a K8s validation error, so "the user's value wins" has to
// mean "the default was never emitted".
func runnerContainer(t *testing.T, rendered string) renderedContainer {
	t.Helper()
	doc := deploymentDocument(t, rendered)
	containers := doc.Spec.Template.Spec.Containers
	if len(containers) != 1 {
		t.Fatalf("containers = %d, want 1:\n%s", len(containers), rendered)
	}
	return containers[0]
}

func deploymentDocument(t *testing.T, rendered string) renderedDeployment {
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
	return doc
}

func TestFullChartPreparesWritableHomesWithoutWeakeningTheRuntime(t *testing.T) {
	for _, test := range []struct {
		name     string
		template string
		path     string
		volume   string
	}{
		{name: "controller PVC", template: "templates/controller-deployment.yaml", path: "/data", volume: "data"},
		{name: "web scratch", template: "templates/web-deployment.yaml", path: "/tmp/sparkwing", volume: "sparkwing-home"},
	} {
		t.Run(test.name, func(t *testing.T) {
			doc := deploymentDocument(t, helmRender(t, "./sparkwing-full", test.template, "sparkwing"))
			pod := doc.Spec.Template.Spec
			if pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot ||
				pod.SecurityContext.RunAsUser == nil || *pod.SecurityContext.RunAsUser != 65534 {
				t.Fatalf("runtime pod security = %+v, want non-root uid 65534", pod.SecurityContext)
			}
			if len(pod.InitContainers) != 1 {
				t.Fatalf("init containers = %d, want one ownership initializer", len(pod.InitContainers))
			}
			init := pod.InitContainers[0]
			if init.Name != "volume-permissions" || !reflect.DeepEqual(init.Command, []string{"/bin/chown"}) ||
				!reflect.DeepEqual(init.Args, []string{"65534:65534", test.path}) {
				t.Fatalf("ownership init = %+v", init)
			}
			if len(pod.Containers) != 1 || init.Image != pod.Containers[0].Image {
				t.Fatalf("ownership image %q does not match runtime image", init.Image)
			}
			security := init.SecurityContext
			if security.RunAsNonRoot == nil || *security.RunAsNonRoot ||
				security.RunAsUser == nil || *security.RunAsUser != 0 ||
				security.RunAsGroup == nil || *security.RunAsGroup != 0 ||
				security.AllowPrivilegeEscalation == nil || *security.AllowPrivilegeEscalation ||
				security.ReadOnlyRootFilesystem == nil || !*security.ReadOnlyRootFilesystem ||
				!reflect.DeepEqual(security.Capabilities.Drop, []string{"ALL"}) ||
				!reflect.DeepEqual(security.Capabilities.Add, []string{"CHOWN"}) {
				t.Fatalf("ownership init security = %+v, want root with CHOWN only", security)
			}
			wantMounts := []renderedVolumeMount{{Name: test.volume, MountPath: test.path}}
			if !reflect.DeepEqual(init.VolumeMounts, wantMounts) {
				t.Fatalf("ownership init mounts = %+v, want %+v", init.VolumeMounts, wantMounts)
			}
		})
	}
}

func TestFullChartVolumePermissionsCanBeDisabled(t *testing.T) {
	for _, template := range []string{"templates/controller-deployment.yaml", "templates/web-deployment.yaml"} {
		doc := deploymentDocument(t, helmRender(t, "./sparkwing-full", template, "sparkwing", "volumePermissions.enabled=false"))
		if len(doc.Spec.Template.Spec.InitContainers) != 0 {
			t.Fatalf("%s rendered ownership init with volumePermissions disabled", template)
		}
	}
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

func renderLogs(t *testing.T, sets ...string) string {
	t.Helper()
	return helmRender(t, "./sparkwing-runner-bundle", "templates/logs-deployment.yaml", "sparkwing", sets...)
}

func renderCache(t *testing.T, sets ...string) string {
	t.Helper()
	return helmRender(t, "./sparkwing-runner-bundle", "templates/cache-deployment.yaml", "sparkwing", sets...)
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
	rendered := renderRunner(t, "cache.enabled=false", "runner.alsoClaimTriggers=false")
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

func TestTriggerClaimingWithoutGitcacheFailsAtRender(t *testing.T) {
	out := helmRenderError(t, "./sparkwing-runner-bundle", "sparkwing", "cache.enabled=false")
	if !strings.Contains(out, "runner.alsoClaimTriggers=true requires cache.enabled=true or runner.extraEnv SPARKWING_GITCACHE_URL") {
		t.Fatalf("render error does not identify the missing gitcache URL:\n%s", out)
	}
}

func TestTriggerClaimingAcceptsAnExternalGitcache(t *testing.T) {
	rendered := renderRunner(t,
		"cache.enabled=false",
		"runner.extraEnv[0].name=SPARKWING_GITCACHE_URL",
		"runner.extraEnv[0].value=https://gitcache.example.com")
	if got := runnerEnv(t, rendered)["SPARKWING_GITCACHE_URL"]; got != "https://gitcache.example.com" {
		t.Errorf("SPARKWING_GITCACHE_URL = %q, want external gitcache URL", got)
	}
	if args := runnerContainer(t, rendered).Args; !containsArg(args, "--also-claim-triggers") {
		t.Errorf("runner args = %v, want trigger claiming preserved", args)
	}
}

func TestRunnerDoesNotAdvertiseAMissingBakedBinary(t *testing.T) {
	env := runnerEnv(t, renderRunner(t))
	if got, exists := env["SPARKWING_BAKED_BINARY"]; exists {
		t.Errorf("SPARKWING_BAKED_BINARY = %q, but the runner image contains no pipeline binary", got)
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

func TestFullChartPointsTheRunnerAtItsController(t *testing.T) {
	rendered := helmRenderInNamespace(t, "./sparkwing-full",
		"charts/sparkwing-runner-bundle/templates/runner-deployment.yaml", "sparkwing", "sparkwing")
	const controllerURL = "http://sparkwing-sparkwing-full-controller.sparkwing.svc.cluster.local"
	if args := runnerContainer(t, rendered).Args; !containsArg(args, "--controller="+controllerURL) {
		t.Errorf("runner args = %v, want controller URL %q", args, controllerURL)
	}
}

func TestFullChartLeavesLogsAuthOffWithoutAToken(t *testing.T) {
	rendered := helmRenderInNamespace(t, "./sparkwing-full",
		"charts/sparkwing-runner-bundle/templates/logs-deployment.yaml", "sparkwing", "sparkwing")
	args := runnerContainer(t, rendered).Args
	if containsArg(args, "--controller") {
		t.Errorf("logs args = %v, want no controller-backed auth in the unauthenticated default install", args)
	}
}

func TestFullChartEnablesLogsAuthAgainstItsController(t *testing.T) {
	rendered := helmRenderInNamespace(t, "./sparkwing-full",
		"charts/sparkwing-runner-bundle/templates/logs-deployment.yaml", "sparkwing", "sparkwing",
		"sparkwing-runner-bundle.controller.tokenSecret.name=sparkwing-token")
	args := runnerContainer(t, rendered).Args
	const controllerURL = "http://sparkwing-sparkwing-full-controller.sparkwing.svc.cluster.local"
	if !containsArg(args, "--controller") || !containsArg(args, controllerURL) {
		t.Errorf("logs args = %v, want controller-backed auth at %q", args, controllerURL)
	}
	if _, exists := runnerEnv(t, rendered)["SPARKWING_API_TOKEN"]; exists {
		t.Error("logs received an unused service bearer instead of validating the caller's Authorization header")
	}
}

func TestLogsControllerURLAloneDoesNotEnableAuth(t *testing.T) {
	args := runnerContainer(t, renderLogs(t, "controller.url=https://controller.example.com")).Args
	if containsArg(args, "--controller") {
		t.Errorf("logs args = %v, want auth disabled without a token Secret", args)
	}
}

func TestFullChartControllerURLOverrideWins(t *testing.T) {
	rendered := helmRender(t, "./sparkwing-full",
		"charts/sparkwing-runner-bundle/templates/runner-deployment.yaml", "sparkwing",
		"nameOverride=wing",
		"sparkwing-runner-bundle.controller.url=https://controller.example.com")
	const want = "--controller=https://controller.example.com"
	if args := runnerContainer(t, rendered).Args; !containsArg(args, want) {
		t.Errorf("runner args = %v, want %q", args, want)
	}
}

func TestFullChartNamingOverrideRequiresControllerURL(t *testing.T) {
	out := helmRenderError(t, "./sparkwing-full", "sparkwing", "fullnameOverride=wing")
	if !strings.Contains(out, "set sparkwing-runner-bundle.controller.url explicitly") {
		t.Fatalf("render error does not identify the required controller URL override:\n%s", out)
	}
}

func TestFullChartNamingOverrideNeedsNoURLWithoutRunnerBundle(t *testing.T) {
	helmRenderAll(t, "./sparkwing-full", "sparkwing", "sparkwing",
		"nameOverride=wing", "sparkwing-runner-bundle.enabled=false")
}

func TestConfiguredSecretRefsAreRequired(t *testing.T) {
	runner := renderRunner(t, "controller.tokenSecret.name=sparkwing-token")
	if strings.Contains(runner, "optional: true") {
		t.Fatalf("configured runner token Secret is optional:\n%s", runner)
	}
	cache := renderCache(t,
		"controller.tokenSecret.name=sparkwing-token",
		"cache.sshKeySecret.name=sparkwing-ssh")
	if strings.Contains(cache, "optional: true") {
		t.Fatalf("configured cache Secret is optional:\n%s", cache)
	}
	for _, name := range []string{"sparkwing-token", "sparkwing-ssh"} {
		if !strings.Contains(cache, "name: \""+name+"\"") && !strings.Contains(cache, "secretName: \""+name+"\"") {
			t.Errorf("cache did not render required Secret %q:\n%s", name, cache)
		}
	}
}

func TestConfiguredSecretNamesRequireKeys(t *testing.T) {
	tests := []struct {
		name  string
		chart string
		sets  []string
		want  string
	}{
		{
			name:  "runner controller token",
			chart: "./sparkwing-runner-bundle",
			sets:  []string{"controller.tokenSecret.name=sparkwing-token", "controller.tokenSecret.key="},
			want:  "controller.tokenSecret.key is required",
		},
		{
			name:  "full chart runner controller token",
			chart: "./sparkwing-full",
			sets: []string{
				"sparkwing-runner-bundle.controller.tokenSecret.name=sparkwing-token",
				"sparkwing-runner-bundle.controller.tokenSecret.key=",
			},
			want: "controller.tokenSecret.key is required",
		},
		{
			name:  "controller webhook",
			chart: "./sparkwing-full",
			sets:  []string{"controller.githubWebhookSecret.name=webhook", "controller.githubWebhookSecret.key="},
			want:  "controller.githubWebhookSecret.key is required",
		},
		{
			name:  "controller encryption",
			chart: "./sparkwing-full",
			sets:  []string{"controller.secretsKey.name=encryption", "controller.secretsKey.key="},
			want:  "controller.secretsKey.key is required",
		},
		{
			name:  "web controller token",
			chart: "./sparkwing-full",
			sets:  []string{"web.tokenSecret.name=web-token", "web.tokenSecret.key="},
			want:  "web.tokenSecret.key is required",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out := helmRenderError(t, test.chart, "sparkwing", test.sets...)
			if !strings.Contains(out, test.want) {
				t.Fatalf("render error does not identify the incomplete Secret reference:\n%s", out)
			}
		})
	}
}

func TestFullChartServiceURLsFollowNestedBundleNaming(t *testing.T) {
	tests := []struct {
		name     string
		set      string
		fullname string
	}{
		{name: "name override", set: "sparkwing-runner-bundle.nameOverride=data", fullname: "sparkwing-data"},
		{name: "fullname override", set: "sparkwing-runner-bundle.fullnameOverride=data-plane", fullname: "data-plane"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			web := webArgs(t, helmTemplate(t, "sparkwing", test.set))
			logsURL := "http://" + test.fullname + "-logs.default.svc.cluster.local"
			cacheURL := "http://" + test.fullname + "-cache.default.svc.cluster.local"
			if got, _ := hasFlag(web, "--logs="); got != "--logs="+logsURL {
				t.Errorf("web logs flag = %q, want nested bundle Service %q", got, logsURL)
			}
			if got, _ := hasFlag(web, "--cache="); got != "--cache="+cacheURL {
				t.Errorf("web cache flag = %q, want nested bundle Service %q", got, cacheURL)
			}

			controller := webArgs(t, helmRender(t, "./sparkwing-full",
				"templates/controller-deployment.yaml", "sparkwing", test.set))
			if got, _ := hasFlag(controller, "--logs-url="); got != "--logs-url="+logsURL {
				t.Errorf("controller logs flag = %q, want nested bundle Service %q", got, logsURL)
			}
		})
	}
}

func TestMaximumLengthReleaseKeepsComponentNamesAndServiceURLsDistinct(t *testing.T) {
	const namespace = "sparkwing-system"
	release := strings.Repeat("r", 53) // Helm's maximum release-name length.
	rendered := helmRenderAll(t, "./sparkwing-full", release, namespace)
	resources := renderedResources(t, rendered)
	assertValidUniqueResourceNames(t, resources)

	controllerService := componentResource(t, resources, "Service", "controller").Metadata.Name
	webService := componentResource(t, resources, "Service", "web").Metadata.Name
	logsService := componentResource(t, resources, "Service", "logs").Metadata.Name
	cacheService := componentResource(t, resources, "Service", "cache").Metadata.Name
	if len(map[string]bool{
		controllerService: true,
		webService:        true,
		logsService:       true,
		cacheService:      true,
	}) != 4 {
		t.Fatalf("component Service names collide: controller=%q web=%q logs=%q cache=%q",
			controllerService, webService, logsService, cacheService)
	}

	serviceURL := func(name string) string {
		return "http://" + name + "." + namespace + ".svc.cluster.local"
	}
	controllerURL := serviceURL(controllerService)
	logsURL := serviceURL(logsService)
	cacheURL := serviceURL(cacheService)
	webArgs := resourceContainer(t, componentResource(t, resources, "Deployment", "web")).Args
	for _, want := range []string{"--controller=" + controllerURL, "--logs=" + logsURL, "--cache=" + cacheURL} {
		if !containsArg(webArgs, want) {
			t.Errorf("web args = %v, want %q", webArgs, want)
		}
	}
	runnerArgs := resourceContainer(t, componentResource(t, resources, "Deployment", "runner")).Args
	for _, want := range []string{"--controller=" + controllerURL, "--logs=" + logsURL, "--gitcache=" + cacheURL} {
		if !containsArg(runnerArgs, want) {
			t.Errorf("runner args = %v, want %q", runnerArgs, want)
		}
	}
	controllerArgs := resourceContainer(t, componentResource(t, resources, "Deployment", "controller")).Args
	if !containsArg(controllerArgs, "--logs-url="+logsURL) {
		t.Errorf("controller args = %v, want logs Service URL %q", controllerArgs, logsURL)
	}
}

func TestMaximumLengthOverridesKeepComponentNamesDistinct(t *testing.T) {
	override := strings.Repeat("o", 63)
	for _, test := range []struct {
		name  string
		chart string
		sets  []string
	}{
		{
			name:  "full nameOverride",
			chart: "./sparkwing-full",
			sets:  []string{"nameOverride=" + override, "sparkwing-runner-bundle.enabled=false"},
		},
		{
			name:  "full fullnameOverride",
			chart: "./sparkwing-full",
			sets:  []string{"fullnameOverride=" + override, "sparkwing-runner-bundle.enabled=false"},
		},
		{
			name:  "runner bundle nameOverride",
			chart: "./sparkwing-runner-bundle",
			sets:  []string{"nameOverride=" + override, "controller.url=https://controller.example.com"},
		},
		{
			name:  "runner bundle fullnameOverride",
			chart: "./sparkwing-runner-bundle",
			sets:  []string{"fullnameOverride=" + override, "controller.url=https://controller.example.com"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			resources := renderedResources(t, helmRenderAll(t, test.chart, "sparkwing", "sparkwing", test.sets...))
			assertValidUniqueResourceNames(t, resources)
			deployments := map[string]string{}
			for _, resource := range resources {
				if resource.Kind == "Deployment" {
					component := resource.Metadata.Labels["app.kubernetes.io/component"]
					deployments[component] = resource.Metadata.Name
				}
			}
			if len(deployments) < 2 {
				t.Fatalf("component Deployments = %v, want multiple distinct names", deployments)
			}
		})
	}
}

func TestParentURLsMatchMaximumLengthBundleOverrides(t *testing.T) {
	const namespace = "sparkwing-system"
	override := strings.Repeat("d", 63)
	rendered := helmRenderAll(t, "./sparkwing-full", "sparkwing", namespace,
		"sparkwing-runner-bundle.fullnameOverride="+override)
	resources := renderedResources(t, rendered)
	assertValidUniqueResourceNames(t, resources)
	logsService := componentResource(t, resources, "Service", "logs").Metadata.Name
	cacheService := componentResource(t, resources, "Service", "cache").Metadata.Name
	if logsService == cacheService || !strings.HasSuffix(logsService, "-logs") || !strings.HasSuffix(cacheService, "-cache") {
		t.Fatalf("bundle Service names do not preserve suffixes: logs=%q cache=%q", logsService, cacheService)
	}
	logsURL := "http://" + logsService + "." + namespace + ".svc.cluster.local"
	cacheURL := "http://" + cacheService + "." + namespace + ".svc.cluster.local"
	webArgs := resourceContainer(t, componentResource(t, resources, "Deployment", "web")).Args
	for _, want := range []string{"--logs=" + logsURL, "--cache=" + cacheURL} {
		if !containsArg(webArgs, want) {
			t.Errorf("web args = %v, want %q", webArgs, want)
		}
	}
	controllerArgs := resourceContainer(t, componentResource(t, resources, "Deployment", "controller")).Args
	if !containsArg(controllerArgs, "--logs-url="+logsURL) {
		t.Errorf("controller args = %v, want logs Service URL %q", controllerArgs, logsURL)
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

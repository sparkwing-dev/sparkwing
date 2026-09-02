package charts

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestFullChartVersion(t *testing.T) {
	data, err := os.ReadFile("sparkwing-full/Chart.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var chart struct {
		Version string `yaml:"version"`
	}
	if err := yaml.Unmarshal(data, &chart); err != nil {
		t.Fatal(err)
	}
	if chart.Version != "0.1.10" {
		t.Fatalf("full chart version = %q, want 0.1.10", chart.Version)
	}
}

func tokenSecretDefault(chart string) string {
	if strings.Contains(chart, "sparkwing-full") {
		return "sparkwing-runner-bundle.controller.tokenSecret.name=sparkwing-token"
	}
	return "controller.tokenSecret.name=sparkwing-token"
}

func helmArgs(chart, release string, sets []string, extra ...string) []string {
	args := append([]string{"template", release, chart}, extra...)
	for _, s := range append([]string{tokenSecretDefault(chart)}, sets...) {
		args = append(args, "--set", s)
	}
	return args
}

func helmRender(t *testing.T, chart, showOnly, release string, sets ...string) string {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not installed; chart rendering not exercised")
	}
	args := helmArgs(chart, release, sets, "--show-only", showOnly)
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
	args := helmArgs(chart, release, sets, "--namespace", namespace, "--show-only", showOnly)
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
	args := helmArgs(chart, release, sets, "--namespace", namespace)
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
	args := helmArgs(chart, release, sets)
	out, err := exec.Command(helm, args...).CombinedOutput()
	if err == nil {
		t.Fatalf("helm %s succeeded, want an actionable render failure", strings.Join(args, " "))
	}
	return string(out)
}

func helmRenderErrorSetString(t *testing.T, chart, release string, setStrings ...string) string {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not installed; chart rendering not exercised")
	}
	args := helmArgs(chart, release, nil)
	for _, s := range setStrings {
		args = append(args, "--set-string", s)
	}
	out, err := exec.Command(helm, args...).CombinedOutput()
	if err == nil {
		t.Fatalf("helm %s succeeded, want an actionable render failure", strings.Join(args, " "))
	}
	return string(out)
}

func helmTemplate(t *testing.T, release string, sets ...string) string {
	t.Helper()
	return helmRender(t, "./sparkwing-full", "templates/web-deployment.yaml", release, sets...)
}

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

	if got, _ := hasFlag(args, "--logs="); got !=
		"--logs=http://sparkwing-sparkwing-runner-bundle-logs.default.svc.cluster.local" {
		t.Errorf("logs flag = %q, want the bundled logs Service", got)
	}
}

func TestWebTrustedProxyCIDRsRenderAsOneFlag(t *testing.T) {
	defaultArgs := webArgs(t, helmTemplate(t, "sparkwing"))
	if got, ok := hasFlag(defaultArgs, "--trusted-proxy-cidrs="); ok {
		t.Fatalf("default trusted proxy flag = %q", got)
	}

	configuredArgs := webArgs(t, helmTemplate(t, "sparkwing",
		"web.trustedProxyCIDRs[0]=10.0.0.0/8",
		"web.trustedProxyCIDRs[1]=192.168.0.0/16",
	))
	got, ok := hasFlag(configuredArgs, "--trusted-proxy-cidrs=")
	if !ok {
		t.Fatalf("no trusted proxy flag in %v", configuredArgs)
	}
	if want := "--trusted-proxy-cidrs=10.0.0.0/8,192.168.0.0/16"; got != want {
		t.Fatalf("trusted proxy flag = %q, want %q", got, want)
	}
}

func TestControllerLoginThrottleFlagsRender(t *testing.T) {
	controllerArgs := func(sets ...string) []string {
		return webArgs(t, helmRender(t, "./sparkwing-full",
			"templates/controller-deployment.yaml", "sparkwing", sets...))
	}

	defaultArgs := controllerArgs()
	if got, ok := hasFlag(defaultArgs, "--trusted-proxy-cidrs="); ok {
		t.Fatalf("default trusted proxy flag = %q", got)
	}
	if got, ok := hasFlag(defaultArgs, "--argon2-memory-budget-mb="); !ok || got != "--argon2-memory-budget-mb=256" {
		t.Fatalf("argon2 budget flag = %q, want the 256 MiB default", got)
	}

	configured := controllerArgs(
		"controller.trustedProxyCIDRs[0]=10.0.0.0/8",
		"controller.trustedProxyCIDRs[1]=192.168.0.0/16",
		"controller.argon2MemoryBudgetMB=128",
	)
	got, ok := hasFlag(configured, "--trusted-proxy-cidrs=")
	if !ok {
		t.Fatalf("no trusted proxy flag in %v", configured)
	}
	if want := "--trusted-proxy-cidrs=10.0.0.0/8,192.168.0.0/16"; got != want {
		t.Fatalf("trusted proxy flag = %q, want %q", got, want)
	}
	if got, _ := hasFlag(configured, "--argon2-memory-budget-mb="); got != "--argon2-memory-budget-mb=128" {
		t.Fatalf("argon2 budget flag = %q, want the override", got)
	}
}

func TestControllerMetricsPortMovesMetricsOffTheAPIPort(t *testing.T) {
	render := func(sets ...string) string {
		return helmRender(t, "./sparkwing-full",
			"templates/controller-deployment.yaml", "sparkwing", sets...)
	}

	byDefault := render()
	if got, ok := hasFlag(webArgs(t, byDefault), "--metrics-addr="); ok {
		t.Fatalf("default metrics flag = %q, want none", got)
	}
	if strings.Contains(byDefault, "name: metrics") {
		t.Fatalf("default Deployment exposes a metrics port:\n%s", byDefault)
	}

	configured := render("controller.metricsPort=9090")
	got, ok := hasFlag(webArgs(t, configured), "--metrics-addr=")
	if !ok || got != "--metrics-addr=:9090" {
		t.Fatalf("metrics flag = %q, want --metrics-addr=:9090", got)
	}
	if !strings.Contains(configured, "containerPort: 9090") {
		t.Fatalf("configured Deployment exposes no metrics container port:\n%s", configured)
	}

	service := helmRender(t, "./sparkwing-full",
		"templates/controller-service.yaml", "sparkwing", "controller.metricsPort=9090")
	if strings.Contains(service, "9090") {
		t.Fatalf("controller Service publishes the metrics port:\n%s", service)
	}
}

func TestWebAddrIsOverridable(t *testing.T) {
	defaultArgs := webArgs(t, helmTemplate(t, "sparkwing"))
	if got, _ := hasFlag(defaultArgs, "--addr="); got != "--addr=0.0.0.0:4343" {
		t.Errorf("default addr flag = %q, want the Service-reachable bind", got)
	}

	loopback := webArgs(t, helmTemplate(t, "sparkwing", "web.addr=127.0.0.1:4343"))
	if got, _ := hasFlag(loopback, "--addr="); got != "--addr=127.0.0.1:4343" {
		t.Errorf("addr flag = %q, want the loopback override", got)
	}
}

func TestWebDeploymentDropsAPIURL(t *testing.T) {
	args := webArgs(t, helmTemplate(t, "sparkwing", "web.apiUrl=https://api.example"))
	if got, ok := hasFlag(args, "--api-url="); ok {
		t.Errorf("api-url flag = %q, want the deprecated flag gone", got)
	}
}

func TestWebCacheURLOverrideWins(t *testing.T) {
	args := webArgs(t, helmTemplate(t, "sparkwing", "web.cache.url=http://cache.elsewhere:8090"))
	if got, _ := hasFlag(args, "--cache="); got != "--cache=http://cache.elsewhere:8090" {
		t.Errorf("cache flag = %q, want the explicit override", got)
	}
}

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

func TestWebCacheURLFollowsTheSubChartNaming(t *testing.T) {
	args := webArgs(t, helmTemplate(t, "sparkwing-runner-bundle"))
	if got, _ := hasFlag(args, "--cache="); got !=
		"--cache=http://sparkwing-runner-bundle-cache.default.svc.cluster.local" {
		t.Errorf("cache flag = %q, want the collapsed release name", got)
	}
}

type renderedEnvVar struct {
	Name      string                  `yaml:"name"`
	Value     string                  `yaml:"value"`
	ValueFrom *renderedEnvValueSource `yaml:"valueFrom"`
}

type renderedEnvValueSource struct {
	SecretKeyRef *renderedSecretKeyRef `yaml:"secretKeyRef"`
}

type renderedSecretKeyRef struct {
	Name     string `yaml:"name"`
	Key      string `yaml:"key"`
	Optional *bool  `yaml:"optional"`
}

type renderedCapabilities struct {
	Add  []string `yaml:"add"`
	Drop []string `yaml:"drop"`
}

type renderedSeccompProfile struct {
	Type string `yaml:"type"`
}

type renderedSecurityContext struct {
	RunAsNonRoot             *bool                   `yaml:"runAsNonRoot"`
	RunAsUser                *int64                  `yaml:"runAsUser"`
	RunAsGroup               *int64                  `yaml:"runAsGroup"`
	AllowPrivilegeEscalation *bool                   `yaml:"allowPrivilegeEscalation"`
	ReadOnlyRootFilesystem   *bool                   `yaml:"readOnlyRootFilesystem"`
	SeccompProfile           *renderedSeccompProfile `yaml:"seccompProfile"`
	Capabilities             renderedCapabilities    `yaml:"capabilities"`
}

type renderedVolumeMount struct {
	Name      string `yaml:"name"`
	MountPath string `yaml:"mountPath"`
}

type renderedVolume struct {
	Name     string         `yaml:"name"`
	EmptyDir map[string]any `yaml:"emptyDir"`
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
				SecurityContext              renderedSecurityContext `yaml:"securityContext"`
				ServiceAccountName           string                  `yaml:"serviceAccountName"`
				AutomountServiceAccountToken *bool                   `yaml:"automountServiceAccountToken"`
				InitContainers               []renderedContainer     `yaml:"initContainers"`
				Containers                   []renderedContainer     `yaml:"containers"`
				Volumes                      []renderedVolume        `yaml:"volumes"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

type renderedPolicyRule struct {
	APIGroups []string `yaml:"apiGroups"`
	Resources []string `yaml:"resources"`
	Verbs     []string `yaml:"verbs"`
}

type renderedResource struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name      string            `yaml:"name"`
		Namespace string            `yaml:"namespace"`
		Labels    map[string]string `yaml:"labels"`
	} `yaml:"metadata"`
	Rules []renderedPolicyRule `yaml:"rules"`
	Spec  struct {
		Template struct {
			Metadata struct {
				Labels map[string]string `yaml:"labels"`
			} `yaml:"metadata"`
			Spec struct {
				ServiceAccountName           string              `yaml:"serviceAccountName"`
				AutomountServiceAccountToken *bool               `yaml:"automountServiceAccountToken"`
				InitContainers               []renderedContainer `yaml:"initContainers"`
				Containers                   []renderedContainer `yaml:"containers"`
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

func TestRunnerBundlePreparesWritableHomeWithoutWeakeningTheRuntime(t *testing.T) {
	doc := deploymentDocument(t, renderRunner(t))
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
		!reflect.DeepEqual(init.Args, []string{"65534:65534", "/tmp/sparkwing"}) {
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
	wantMounts := []renderedVolumeMount{{Name: "sparkwing-home", MountPath: "/tmp/sparkwing"}}
	if !reflect.DeepEqual(init.VolumeMounts, wantMounts) {
		t.Fatalf("ownership init mounts = %+v, want %+v", init.VolumeMounts, wantMounts)
	}
}

func TestRunnerBundleVolumePermissionsCanBeDisabled(t *testing.T) {
	doc := deploymentDocument(t, renderRunner(t, "volumePermissions.enabled=false"))
	if len(doc.Spec.Template.Spec.InitContainers) != 0 {
		t.Fatal("runner rendered ownership init with volumePermissions disabled")
	}
}

func TestFullChartVendorsRunnerVolumePermissions(t *testing.T) {
	resources := renderedResources(t, helmRenderAll(t, "./sparkwing-full", "sparkwing", "default"))
	runner := componentResource(t, resources, "Deployment", "runner")
	if len(runner.Spec.Template.Spec.InitContainers) != 1 {
		t.Fatalf("vendored runner init containers = %d, want one", len(runner.Spec.Template.Spec.InitContainers))
	}
	init := runner.Spec.Template.Spec.InitContainers[0]
	if init.Name != "volume-permissions" || !reflect.DeepEqual(init.Args, []string{"65534:65534", "/tmp/sparkwing"}) {
		t.Fatalf("vendored runner ownership init = %+v", init)
	}

	resources = renderedResources(t, helmRenderAll(t, "./sparkwing-full", "sparkwing", "default",
		"sparkwing-runner-bundle.volumePermissions.enabled=false"))
	runner = componentResource(t, resources, "Deployment", "runner")
	if len(runner.Spec.Template.Spec.InitContainers) != 0 {
		t.Fatal("vendored runner rendered ownership init with volumePermissions disabled")
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

func renderRunnerInNamespace(t *testing.T, namespace string, sets ...string) string {
	t.Helper()
	return helmRenderInNamespace(t, "./sparkwing-runner-bundle", "templates/runner-deployment.yaml", "sparkwing", namespace, sets...)
}

func TestRunnerTriggerRunnerDefaultsToInProcess(t *testing.T) {
	args := runnerContainer(t, renderRunner(t)).Args
	for _, arg := range args {
		if strings.HasPrefix(arg, "--trigger-runner") || arg == "--claim-nodes=false" {
			t.Errorf("default runner args include warm execution flag %q", arg)
		}
	}

	resources := renderedResources(t, helmRenderAll(t, "./sparkwing-runner-bundle", "sparkwing", "default"))
	for _, resource := range resources {
		for _, rule := range resource.Rules {
			if reflect.DeepEqual(rule.Resources, []string{"jobs"}) {
				t.Fatalf("default %s/%s grants Job mutation: %+v", resource.Kind, resource.Metadata.Name, rule)
			}
		}
	}
}

func TestRunnerWarmTriggerRunnerUsesRemoteCapacityBeforeKubernetes(t *testing.T) {
	args := runnerContainer(t, renderRunnerInNamespace(t, "capacity",
		"runner.triggerRunner.kind=warm",
		"runner.automountServiceAccountToken=true",
		"runner.image.repository=registry.example/sparkwing-runner",
		"runner.image.tag=remote",
		"runner.image.pullPolicy=Always",
		"serviceAccount.create=false",
		"serviceAccount.name=remote-fallback",
		"serviceAccount.shareAcrossComponents=true",
	)).Args
	want := []string{
		"--claim-nodes=false",
		"--trigger-runner=warm",
		"--trigger-runner-namespace=capacity",
		"--trigger-runner-image=registry.example/sparkwing-runner:remote",
		"--trigger-runner-sa=remote-fallback",
		"--trigger-runner-image-pull-policy=Always",
		"--trigger-artifact-store=http://sparkwing-sparkwing-runner-bundle-cache.capacity.svc.cluster.local",
	}
	for _, arg := range want {
		if !containsArg(args, arg) {
			t.Errorf("warm runner args = %v, want %q", args, arg)
		}
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "--trigger-warm-") || strings.HasPrefix(arg, "--warm-claim-") || strings.HasPrefix(arg, "--warm-poll") {
			t.Errorf("warm runner args expose internal timing flag %q", arg)
		}
	}
}

func TestRunnerWarmTriggerRunnerGrantsNamespaceJobCRUD(t *testing.T) {
	resources := renderedResources(t, helmRenderAll(t, "./sparkwing-runner-bundle", "sparkwing", "capacity",
		"runner.triggerRunner.kind=warm", "runner.automountServiceAccountToken=true"))
	var jobRule *renderedPolicyRule
	var podRule *renderedPolicyRule
	for i := range resources {
		if resources[i].Kind == "ClusterRole" {
			t.Fatalf("warm mode rendered cluster-scoped RBAC: %+v", resources[i].Metadata)
		}
		if resources[i].Kind != "Role" {
			continue
		}
		if resources[i].Metadata.Namespace != "capacity" {
			t.Fatalf("Role namespace = %q, want capacity", resources[i].Metadata.Namespace)
		}
		for j := range resources[i].Rules {
			if reflect.DeepEqual(resources[i].Rules[j].Resources, []string{"jobs"}) {
				jobRule = &resources[i].Rules[j]
			} else if reflect.DeepEqual(resources[i].Rules[j].Resources, []string{"pods"}) {
				podRule = &resources[i].Rules[j]
			} else {
				t.Errorf("warm mode grants unrelated resources: %+v", resources[i].Rules[j])
			}
		}
	}
	if jobRule == nil {
		t.Fatal("warm mode rendered no Job rule")
	}
	if !reflect.DeepEqual(jobRule.APIGroups, []string{"batch"}) ||
		!reflect.DeepEqual(jobRule.Verbs, []string{"create", "get", "delete"}) {
		t.Fatalf("Job rule = %+v, want the exact fallback lifecycle verbs", *jobRule)
	}
	if podRule == nil || len(podRule.APIGroups) != 1 || podRule.APIGroups[0] != "" ||
		!reflect.DeepEqual(podRule.Verbs, []string{"list"}) {
		t.Fatalf("Pod rule = %+v, want the exact fallback inspection verb", podRule)
	}
}

func TestRunnerClusterTriggerRunnersRequireAnAPIToken(t *testing.T) {
	for _, kind := range []string{"k8s", "warm"} {
		out := helmRenderError(t, "./sparkwing-runner-bundle", "sparkwing", "runner.triggerRunner.kind="+kind)
		if !strings.Contains(out, "runner.automountServiceAccountToken=true") {
			t.Errorf("%s render error does not name the required token setting:\n%s", kind, out)
		}
	}
}

func TestRunnerClusterTriggerRunnersRequireTheTriggerLoop(t *testing.T) {
	out := helmRenderError(t, "./sparkwing-runner-bundle", "sparkwing",
		"runner.triggerRunner.kind=warm", "runner.alsoClaimTriggers=false")
	if !strings.Contains(out, "runner.alsoClaimTriggers=true") {
		t.Fatalf("render error does not name the required trigger loop:\n%s", out)
	}
}

func TestRunnerTriggerRunnerKindRejectsInvalidValue(t *testing.T) {
	out := helmRenderError(t, "./sparkwing-runner-bundle", "sparkwing", "runner.triggerRunner.kind=remote")
	if !strings.Contains(out, "runner.triggerRunner.kind must be inprocess, k8s, or warm") {
		t.Fatalf("render error does not identify trigger runner kinds:\n%s", out)
	}
}

func TestRunnerWarmTimingIsNotChartConfiguration(t *testing.T) {
	for _, path := range []string{"sparkwing-runner-bundle/values.yaml", "sparkwing-full/values.yaml"} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, removed := range []string{"warmClaimWait", "warmPollInterval", "claimNodes"} {
			if strings.Contains(string(body), removed) {
				t.Errorf("%s exposes removed setting %q", path, removed)
			}
		}
	}
}

func renderController(t *testing.T, sets ...string) renderedContainer {
	t.Helper()
	rendered := helmRender(t, "./sparkwing-full", "templates/controller-deployment.yaml", "sparkwing", sets...)
	return runnerContainer(t, rendered)
}

func TestWebConfiguredControllerTokenIsRequired(t *testing.T) {
	web := runnerContainer(t, helmTemplate(t, "sparkwing", "web.tokenSecret.name=sparkwing-token"))
	for _, env := range web.Env {
		if env.Name != "SPARKWING_AGENT_TOKEN" {
			continue
		}
		if env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
			t.Fatalf("SPARKWING_AGENT_TOKEN secretKeyRef missing: %+v", env)
		}
		ref := env.ValueFrom.SecretKeyRef
		if ref.Name != "sparkwing-token" || ref.Key != "token" {
			t.Errorf("SPARKWING_AGENT_TOKEN secretKeyRef = %+v", ref)
		}
		if ref.Optional != nil && *ref.Optional {
			t.Fatal("SPARKWING_AGENT_TOKEN secretKeyRef is optional")
		}
		return
	}
	t.Fatal("SPARKWING_AGENT_TOKEN env missing")
}

func webTokenSecretRef(t *testing.T, sets ...string) *renderedSecretKeyRef {
	t.Helper()
	for _, env := range runnerContainer(t, helmTemplate(t, "sparkwing", sets...)).Env {
		if env.Name != "SPARKWING_AGENT_TOKEN" {
			continue
		}
		if env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
			t.Fatalf("SPARKWING_AGENT_TOKEN carries no secretKeyRef: %+v", env)
		}
		return env.ValueFrom.SecretKeyRef
	}
	return nil
}

func TestWebInheritsTheBundlesControllerTokenSecret(t *testing.T) {
	ref := webTokenSecretRef(t,
		"sparkwing-runner-bundle.controller.tokenSecret.name=bundle-token",
		"sparkwing-runner-bundle.controller.tokenSecret.key=bearer")
	if ref == nil {
		t.Fatal("web pod carries no bearer while the logs service validates one; every dashboard log pane would 401")
	}
	if ref.Name != "bundle-token" || ref.Key != "bearer" {
		t.Errorf("SPARKWING_AGENT_TOKEN secretKeyRef = %+v, want the bundle's Secret", ref)
	}
}

func TestWebTokenSecretOverridesTheBundleDefault(t *testing.T) {
	ref := webTokenSecretRef(t,
		"sparkwing-runner-bundle.controller.tokenSecret.name=bundle-token",
		"web.tokenSecret.name=web-token")
	if ref == nil || ref.Name != "web-token" || ref.Key != "token" {
		t.Errorf("SPARKWING_AGENT_TOKEN secretKeyRef = %+v, want the explicit web Secret", ref)
	}
}

func TestWebCarriesNoTokenOnAnOptedOutInstall(t *testing.T) {
	if ref := webTokenSecretRef(t,
		"sparkwing-runner-bundle.controller.tokenSecret.name=",
		"sparkwing-runner-bundle.cache.allowUnauthenticated=true",
		"sparkwing-runner-bundle.logs.allowUnauthenticated=true"); ref != nil {
		t.Errorf("SPARKWING_AGENT_TOKEN secretKeyRef = %+v, want no Secret reference when none is configured", ref)
	}
}

func TestControllerGitHubStatusEnvironment(t *testing.T) {
	defaultController := renderController(t)
	for _, env := range defaultController.Env {
		if env.Name == "GITHUB_TOKEN" || env.Name == "SPARKWING_DASHBOARD_URL" {
			t.Errorf("default controller unexpectedly sets %s", env.Name)
		}
	}

	configured := renderController(t,
		"controller.githubStatusToken.name=github-status",
		"controller.githubStatusToken.key=credential",
		"controller.dashboardURL=https://sparkwing.example.com/team",
	)
	var token *renderedEnvVar
	dashboardURL := ""
	for i := range configured.Env {
		switch configured.Env[i].Name {
		case "GITHUB_TOKEN":
			token = &configured.Env[i]
		case "SPARKWING_DASHBOARD_URL":
			dashboardURL = configured.Env[i].Value
		}
	}
	if token == nil || token.ValueFrom == nil || token.ValueFrom.SecretKeyRef == nil {
		t.Fatalf("GITHUB_TOKEN secretKeyRef missing: %+v", token)
	}
	if got := *token.ValueFrom.SecretKeyRef; got.Name != "github-status" || got.Key != "credential" {
		t.Errorf("GITHUB_TOKEN secretKeyRef = %+v", got)
	}
	if dashboardURL != "https://sparkwing.example.com/team" {
		t.Errorf("SPARKWING_DASHBOARD_URL = %q", dashboardURL)
	}
}

func renderLogs(t *testing.T, sets ...string) string {
	t.Helper()
	return helmRender(t, "./sparkwing-runner-bundle", "templates/logs-deployment.yaml", "sparkwing", sets...)
}

func renderCache(t *testing.T, sets ...string) string {
	t.Helper()
	return helmRender(t, "./sparkwing-runner-bundle", "templates/cache-deployment.yaml", "sparkwing", sets...)
}

func TestCachePinsAWritableHome(t *testing.T) {
	if got := runnerEnv(t, renderCache(t))["HOME"]; got != "/tmp" {
		t.Fatalf("cache HOME = %q, want /tmp so the SSH key stages on the scratch volume", got)
	}
}

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

func TestCachePublicURLMatchesTheProxyURLRunnersDial(t *testing.T) {
	const want = "http://sparkwing-sparkwing-runner-bundle-cache.default.svc.cluster.local"
	if got := runnerEnv(t, renderCache(t))["SPARKWING_CACHE_PUBLIC_URL"]; got != want {
		t.Errorf("SPARKWING_CACHE_PUBLIC_URL = %q, want %q", got, want)
	}
	if got := runnerEnv(t, renderRunner(t))["npm_config_registry"]; got != want+"/proxy/npm" {
		t.Errorf("npm_config_registry = %q, want the same base the cache rewrites against", got)
	}
}

func TestCachePublicURLOverrideWins(t *testing.T) {
	env := runnerEnv(t, renderCache(t, "cache.publicUrl=https://cache.example.com"))
	if got := env["SPARKWING_CACHE_PUBLIC_URL"]; got != "https://cache.example.com" {
		t.Errorf("SPARKWING_CACHE_PUBLIC_URL = %q, want the explicit override", got)
	}
}

func TestCachePublicURLIsUnsetWhenClientsDialMoreThanOneAddress(t *testing.T) {
	for _, serviceType := range []string{"LoadBalancer", "NodePort"} {
		env := runnerEnv(t, renderCache(t, "cache.service.type="+serviceType))
		if got, ok := env["SPARKWING_CACHE_PUBLIC_URL"]; ok {
			t.Errorf("%s Service set SPARKWING_CACHE_PUBLIC_URL = %q, want it unset so the proxy rewrites per request", serviceType, got)
		}
	}

	env := runnerEnv(t, renderCache(t,
		"cache.service.type=LoadBalancer",
		"cache.publicUrl=http://cache.example.com"))
	if got := env["SPARKWING_CACHE_PUBLIC_URL"]; got != "http://cache.example.com" {
		t.Errorf("SPARKWING_CACHE_PUBLIC_URL = %q, want the explicit override", got)
	}
}

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

func TestFullChartCarriesTheDependencyProxyWiring(t *testing.T) {
	env := runnerEnv(t, helmRender(t, "./sparkwing-full",
		"charts/sparkwing-runner-bundle/templates/runner-deployment.yaml", "sparkwing"))
	const host = "sparkwing-sparkwing-runner-bundle-cache.default.svc.cluster.local"
	if got := env["GOPROXY"]; got != "http://"+host+"/proxy/golang|https://proxy.golang.org,direct" {
		t.Errorf("GOPROXY = %q; the vendored sub-chart may be stale. "+
			"Fix: helm dep up ./charts/sparkwing-full", got)
	}
}

func TestFullChartVendorsWarmTriggerRunner(t *testing.T) {
	rendered := helmRenderInNamespace(t, "./sparkwing-full",
		"charts/sparkwing-runner-bundle/templates/runner-deployment.yaml", "sparkwing", "capacity",
		"sparkwing-runner-bundle.runner.triggerRunner.kind=warm",
		"sparkwing-runner-bundle.runner.automountServiceAccountToken=true")
	args := runnerContainer(t, rendered).Args
	for _, want := range []string{
		"--trigger-runner=warm",
		"--claim-nodes=false",
		"--trigger-runner-namespace=capacity",
	} {
		if !containsArg(args, want) {
			t.Errorf("vendored runner args = %v, want %q", args, want)
		}
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

func TestFullChartLogsWithoutATokenSecretFailsAtRender(t *testing.T) {
	out := helmRenderError(t, "./sparkwing-full", "sparkwing",
		"sparkwing-runner-bundle.controller.tokenSecret.name=",
		"sparkwing-runner-bundle.cache.allowUnauthenticated=true")
	for _, want := range []string{"controller.tokenSecret.name", "logs.allowUnauthenticated=true"} {
		if !strings.Contains(out, want) {
			t.Errorf("render error does not name %q:\n%s", want, out)
		}
	}
}

func TestFullChartLeavesLogsAuthOffOnlyWhenTheOperatorOptsOut(t *testing.T) {
	rendered := helmRenderInNamespace(t, "./sparkwing-full",
		"charts/sparkwing-runner-bundle/templates/logs-deployment.yaml", "sparkwing", "sparkwing",
		"sparkwing-runner-bundle.controller.tokenSecret.name=",
		"sparkwing-runner-bundle.cache.allowUnauthenticated=true",
		"sparkwing-runner-bundle.logs.allowUnauthenticated=true")
	args := runnerContainer(t, rendered).Args
	if containsArg(args, "--controller") || containsArg(args, "--require-auth") {
		t.Errorf("logs args = %v, want an opted-out logs service to carry no auth flags", args)
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
	args := runnerContainer(t, renderLogs(t,
		"controller.url=https://controller.example.com",
		"controller.tokenSecret.name=",
		"cache.allowUnauthenticated=true",
		"logs.allowUnauthenticated=true")).Args
	if containsArg(args, "--controller") {
		t.Errorf("logs args = %v, want auth disabled without a token Secret", args)
	}
}

func TestLogsWithoutATokenSecretFailsAtRender(t *testing.T) {
	out := helmRenderError(t, "./sparkwing-runner-bundle", "sparkwing",
		"controller.tokenSecret.name=", "cache.allowUnauthenticated=true")
	for _, want := range []string{"controller.tokenSecret.name", "logs.allowUnauthenticated=true"} {
		if !strings.Contains(out, want) {
			t.Errorf("render error does not name %q:\n%s", want, out)
		}
	}
}

func TestLogsRefusesToStartUnauthenticatedByDefault(t *testing.T) {
	if args := runnerContainer(t, renderLogs(t)).Args; !containsArg(args, "--require-auth") {
		t.Errorf("logs args = %v, want --require-auth so an open pod crashes instead of serving", args)
	}
	args := runnerContainer(t, renderLogs(t,
		"controller.tokenSecret.name=",
		"cache.allowUnauthenticated=true",
		"logs.allowUnauthenticated=true")).Args
	if containsArg(args, "--require-auth") {
		t.Errorf("logs args = %v, want no startup guard once the operator opts out", args)
	}
}

func TestLogsQuotaFlagsComeFromValues(t *testing.T) {
	if args := runnerContainer(t, renderLogs(t)).Args; containsArg(args, "--max-node-bytes") {
		t.Errorf("logs args = %v, want the binary's own defaults when the operator sets no quota", args)
	}
	args := runnerContainer(t, renderLogs(t,
		"logs.limits.maxNodeBytes=1048576",
		"logs.limits.maxInflightBytes=0",
		"logs.limits.retention=168h")).Args
	for flag, want := range map[string]string{
		"--max-node-bytes":     "1048576",
		"--max-inflight-bytes": "0",
		"--retention":          "168h",
	} {
		if !containsArg(args, flag) {
			t.Errorf("logs args = %v, want %s", args, flag)
			continue
		}
		if got := argValue(args, flag); got != want {
			t.Errorf("%s = %q, want %q", flag, got, want)
		}
	}
	if containsArg(args, "--max-run-bytes") {
		t.Errorf("logs args = %v, want no flag for a quota the operator left empty", args)
	}
}

func argValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
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
	const maxHelmReleaseNameLength = 53
	release := strings.Repeat("r", maxHelmReleaseNameLength)
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

func TestControllerAnnouncesNoLogsServiceWhenNoneIsDeployed(t *testing.T) {
	rendered := helmRender(t, "./sparkwing-full", "templates/controller-deployment.yaml", "sparkwing",
		"sparkwing-runner-bundle.logs.enabled=false")
	args := webArgs(t, rendered)
	if got, ok := hasFlag(args, "--logs-url="); ok {
		t.Errorf("got %q, want no flag when no logs service is deployed", got)
	}
}

func TestRunnerBundleRoleGrantsNoNamespaceReads(t *testing.T) {
	resources := renderedResources(t, helmRenderAll(t, "./sparkwing-runner-bundle", "sparkwing", "default",
		"controller.url=http://controller"))
	var roles int
	for _, resource := range resources {
		if resource.Kind != "Role" && resource.Kind != "ClusterRole" {
			continue
		}
		roles++
		for _, rule := range resource.Rules {
			t.Errorf("%s %s grants %v on %v; the runner reads none of it through the API",
				resource.Kind, resource.Metadata.Name, rule.Verbs, rule.Resources)
		}
	}
	if roles != 1 {
		t.Fatalf("rendered %d runner Roles, want exactly one", roles)
	}
}

func TestRunnerBundleMountsNoServiceAccountTokens(t *testing.T) {
	resources := renderedResources(t, helmRenderAll(t, "./sparkwing-runner-bundle", "sparkwing", "default",
		"controller.url=http://controller"))
	accounts := map[string]string{}
	for _, component := range []string{"runner", "cache", "logs"} {
		deployment := componentResource(t, resources, "Deployment", component)
		pod := deployment.Spec.Template.Spec
		if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
			t.Errorf("%s pod automounts a ServiceAccount token", component)
		}
		if pod.ServiceAccountName == "" {
			t.Errorf("%s pod names no ServiceAccount", component)
		}
		if owner, dup := accounts[pod.ServiceAccountName]; dup {
			t.Errorf("%s shares ServiceAccount %q with %s", component, pod.ServiceAccountName, owner)
		}
		accounts[pod.ServiceAccountName] = component
	}
	created := map[string]bool{}
	for _, resource := range resources {
		if resource.Kind == "ServiceAccount" {
			created[resource.Metadata.Name] = true
		}
	}
	for name, component := range accounts {
		if !created[name] {
			t.Errorf("%s names ServiceAccount %q that the chart never creates", component, name)
		}
	}
}

func TestFullChartMountsNoWebServiceAccountToken(t *testing.T) {
	pod := deploymentDocument(t, helmTemplate(t, "sparkwing")).Spec.Template.Spec
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Fatal("web pod automounts a ServiceAccount token")
	}
}

func TestFullChartScopesTheWarmerServiceAccountToTheRelease(t *testing.T) {
	const want = "other-sparkwing-full-cache-warmer"
	resources := renderedResources(t, helmRenderAll(t, "./sparkwing-full", "other", "default"))
	var created bool
	for _, resource := range resources {
		if resource.Kind == "ServiceAccount" && resource.Metadata.Name == want {
			created = true
		}
	}
	if !created {
		t.Errorf("no %s ServiceAccount for the controller's warmer pods", want)
	}
	controller := componentResource(t, resources, "Deployment", "controller")
	args := resourceContainer(t, controller).Args
	got, ok := hasFlag(args, "--warmer-service-account=")
	if !ok {
		t.Fatalf("no --warmer-service-account flag in %v; the controller would name the unscoped default", args)
	}
	if got != "--warmer-service-account="+want {
		t.Errorf("warmer-service-account flag = %q, want the release-scoped account", got)
	}
}

func TestFullChartWarmerServiceAccountDoesNotCollideAcrossReleases(t *testing.T) {
	names := map[string]bool{}
	for _, release := range []string{"sparkwing", "other"} {
		for _, resource := range renderedResources(t, helmRenderAll(t, "./sparkwing-full", release, "default")) {
			if resource.Kind != "ServiceAccount" {
				continue
			}
			if names[resource.Metadata.Name] {
				t.Errorf("ServiceAccount %q renders for both releases; a second install into one namespace fails", resource.Metadata.Name)
			}
			names[resource.Metadata.Name] = true
		}
	}
}

func TestRunnerBundleCanRemountTheRunnerToken(t *testing.T) {
	resources := renderedResources(t, helmRenderAll(t, "./sparkwing-runner-bundle", "sparkwing", "default",
		"controller.url=http://controller", "runner.automountServiceAccountToken=true"))
	pod := componentResource(t, resources, "Deployment", "runner").Spec.Template.Spec
	if pod.AutomountServiceAccountToken == nil || !*pod.AutomountServiceAccountToken {
		t.Fatalf("runner automountServiceAccountToken = %v, want true; the k8s trigger runner cannot load its in-cluster config without a token",
			pod.AutomountServiceAccountToken)
	}
	for _, component := range []string{"cache", "logs"} {
		other := componentResource(t, resources, "Deployment", component).Spec.Template.Spec
		if other.AutomountServiceAccountToken == nil || *other.AutomountServiceAccountToken {
			t.Errorf("%s pod automounts a ServiceAccount token when only the runner opts in", component)
		}
	}
}

func TestRunnerBundleRefusesSilentServiceAccountSharing(t *testing.T) {
	out := helmRenderError(t, "./sparkwing-runner-bundle", "sparkwing",
		"controller.url=http://controller", "serviceAccount.create=false", "serviceAccount.name=my-existing-sa")
	if !strings.Contains(out, "shareAcrossComponents") {
		t.Fatalf("render failure does not name the acknowledgement value:\n%s", out)
	}
	resources := renderedResources(t, helmRenderAll(t, "./sparkwing-runner-bundle", "sparkwing", "default",
		"controller.url=http://controller", "serviceAccount.create=false", "serviceAccount.name=my-existing-sa",
		"serviceAccount.shareAcrossComponents=true"))
	for _, component := range []string{"runner", "cache", "logs"} {
		pod := componentResource(t, resources, "Deployment", component).Spec.Template.Spec
		if pod.ServiceAccountName != "my-existing-sa" {
			t.Errorf("%s serviceAccountName = %q, want the operator's pre-created account", component, pod.ServiceAccountName)
		}
	}
}

func TestFullChartVendorsTheTightenedRunnerRBAC(t *testing.T) {
	resources := renderedResources(t, helmRenderAll(t, "./sparkwing-full", "sparkwing", "default"))
	runner := componentResource(t, resources, "Deployment", "runner")
	if pod := runner.Spec.Template.Spec; pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Error("vendored runner pod automounts a ServiceAccount token")
	}
	for _, resource := range resources {
		if resource.Kind != "Role" {
			continue
		}
		for _, rule := range resource.Rules {
			for _, res := range rule.Resources {
				if res == "secrets" {
					t.Errorf("Role %s still grants %v on secrets", resource.Metadata.Name, rule.Verbs)
				}
			}
		}
	}
}

func envSecretRef(t *testing.T, rendered, envName string) renderedSecretKeyRef {
	t.Helper()
	for _, env := range runnerContainer(t, rendered).Env {
		if env.Name != envName {
			continue
		}
		if env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
			t.Fatalf("%s is not a secretKeyRef: %+v", envName, env)
		}
		return *env.ValueFrom.SecretKeyRef
	}
	t.Fatalf("%s env missing:\n%s", envName, rendered)
	return renderedSecretKeyRef{}
}

func TestRunnerAndCacheShareOneCacheTokenSecret(t *testing.T) {
	sets := []string{
		"controller.tokenSecret.name=sparkwing-token",
		"controller.tokenSecret.key=bearer",
	}
	runner := envSecretRef(t, renderRunner(t, sets...), "SPARKWING_CACHE_TOKEN")
	cache := envSecretRef(t, renderCache(t, sets...), "SPARKWING_API_TOKEN")
	if runner != cache {
		t.Fatalf("runner SPARKWING_CACHE_TOKEN = %+v, cache SPARKWING_API_TOKEN = %+v; want one source", runner, cache)
	}
	if runner.Name != "sparkwing-token" || runner.Key != "bearer" {
		t.Errorf("token source = %+v, want the configured Secret and key", runner)
	}
}

func TestFullChartVendorsTheRunnerCacheToken(t *testing.T) {
	rendered := helmRenderInNamespace(t, "./sparkwing-full",
		"charts/sparkwing-runner-bundle/templates/runner-deployment.yaml", "sparkwing", "sparkwing",
		"sparkwing-runner-bundle.controller.tokenSecret.name=sparkwing-token")
	if ref := envSecretRef(t, rendered, "SPARKWING_CACHE_TOKEN"); ref.Name != "sparkwing-token" {
		t.Errorf("SPARKWING_CACHE_TOKEN source = %+v, want the release token Secret", ref)
	}
}

func TestCacheWithoutATokenSecretFailsAtRender(t *testing.T) {
	out := helmRenderError(t, "./sparkwing-runner-bundle", "sparkwing", "controller.tokenSecret.name=")
	for _, want := range []string{"controller.tokenSecret.name", "cache.allowUnauthenticated=true"} {
		if !strings.Contains(out, want) {
			t.Errorf("render error does not name %q:\n%s", want, out)
		}
	}
}

func TestCacheAllowUnauthenticatedRendersTheOptIn(t *testing.T) {
	rendered := renderCache(t, "controller.tokenSecret.name=",
		"cache.allowUnauthenticated=true", "logs.allowUnauthenticated=true")
	if args := runnerContainer(t, rendered).Args; !containsArg(args, "--allow-unauthenticated") {
		t.Errorf("cache args = %v, want --allow-unauthenticated", args)
	}
	if args := runnerContainer(t, renderCache(t)).Args; containsArg(args, "--allow-unauthenticated") {
		t.Errorf("cache args = %v, want no unauthenticated opt-in by default", args)
	}
}

type renderedNetworkPolicy struct {
	Kind string `yaml:"kind"`
	Spec struct {
		PodSelector struct {
			MatchLabels map[string]string `yaml:"matchLabels"`
		} `yaml:"podSelector"`
		PolicyTypes []string `yaml:"policyTypes"`
		Ingress     []struct {
			From []struct {
				PodSelector struct {
					MatchLabels map[string]string `yaml:"matchLabels"`
				} `yaml:"podSelector"`
			} `yaml:"from"`
			Ports []struct {
				Protocol string `yaml:"protocol"`
				Port     int    `yaml:"port"`
			} `yaml:"ports"`
		} `yaml:"ingress"`
	} `yaml:"spec"`
}

func renderCacheNetworkPolicy(t *testing.T, sets ...string) renderedNetworkPolicy {
	t.Helper()
	rendered := helmRender(t, "./sparkwing-runner-bundle",
		"templates/cache-networkpolicy.yaml", "sparkwing", sets...)
	var doc renderedNetworkPolicy
	decoder := yaml.NewDecoder(strings.NewReader(rendered))
	for {
		var candidate renderedNetworkPolicy
		if err := decoder.Decode(&candidate); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("decode NetworkPolicy: %v\n%s", err, rendered)
		}
		if candidate.Kind == "NetworkPolicy" {
			doc = candidate
		}
	}
	if doc.Kind != "NetworkPolicy" {
		t.Fatalf("no NetworkPolicy rendered:\n%s", rendered)
	}
	return doc
}

func TestCacheNetworkPolicyAdmitsEveryCacheClient(t *testing.T) {
	policy := renderCacheNetworkPolicy(t)

	if !reflect.DeepEqual(policy.Spec.PolicyTypes, []string{"Ingress"}) {
		t.Errorf("policyTypes = %v, want [Ingress]", policy.Spec.PolicyTypes)
	}
	if got := policy.Spec.PodSelector.MatchLabels["app.kubernetes.io/component"]; got != "cache" {
		t.Errorf("policy selects component %q, want cache", got)
	}
	if len(policy.Spec.Ingress) != 1 {
		t.Fatalf("ingress rules = %d, want one default-deny rule listing every peer", len(policy.Spec.Ingress))
	}
	rule := policy.Spec.Ingress[0]
	want := []map[string]string{
		{
			"app.kubernetes.io/name":      "sparkwing-runner-bundle",
			"app.kubernetes.io/instance":  "sparkwing",
			"app.kubernetes.io/component": "runner",
		},
		{
			"app.kubernetes.io/instance":  "sparkwing",
			"app.kubernetes.io/component": "controller",
		},
		{
			"app.kubernetes.io/instance":  "sparkwing",
			"app.kubernetes.io/component": "web",
		},
		{
			"app.kubernetes.io/name": "sparkwing-runner",
		},
	}
	if len(rule.From) != len(want) {
		t.Fatalf("ingress peers = %d, want the runner, controller, dashboard, and k8s runner Job pods", len(rule.From))
	}
	for i, peer := range want {
		if got := rule.From[i].PodSelector.MatchLabels; !reflect.DeepEqual(got, peer) {
			t.Errorf("ingress peer %d = %v, want %v", i, got, peer)
		}
	}
	if len(rule.Ports) != 1 || rule.Ports[0].Port != 8090 || rule.Ports[0].Protocol != "TCP" {
		t.Errorf("ingress ports = %+v, want TCP 8090", rule.Ports)
	}
}

func TestCacheNetworkPolicyTakesAnExplicitControllerSelector(t *testing.T) {
	policy := renderCacheNetworkPolicy(t, `networkPolicy.controllerPodSelector.app\.kubernetes\.io/name=my-controller`)
	want := map[string]string{"app.kubernetes.io/name": "my-controller"}
	if got := policy.Spec.Ingress[0].From[1].PodSelector.MatchLabels; !reflect.DeepEqual(got, want) {
		t.Errorf("controller peer = %v, want %v", got, want)
	}
}

func TestCacheNetworkPolicyTakesAnExplicitWebSelector(t *testing.T) {
	policy := renderCacheNetworkPolicy(t, `networkPolicy.webPodSelector.app\.kubernetes\.io/name=my-dashboard`)
	want := map[string]string{"app.kubernetes.io/name": "my-dashboard"}
	if got := policy.Spec.Ingress[0].From[2].PodSelector.MatchLabels; !reflect.DeepEqual(got, want) {
		t.Errorf("web peer = %v, want %v", got, want)
	}
}

func TestCacheNetworkPolicyAppendsExtraIngressForAnOffClusterCaller(t *testing.T) {
	rendered := helmRender(t, "./sparkwing-runner-bundle", "templates/cache-networkpolicy.yaml", "sparkwing",
		"controller.tokenSecret.name=tok",
		"networkPolicy.extraIngress[0].from[0].ipBlock.cidr=10.42.0.0/16",
		"networkPolicy.extraIngress[0].ports[0].protocol=TCP",
		"networkPolicy.extraIngress[0].ports[0].port=8090")
	if !strings.Contains(rendered, "10.42.0.0/16") {
		t.Errorf("extraIngress rule missing from the rendered policy:\n%s", rendered)
	}
}

func TestFullChartCacheNetworkPolicyAdmitsTheDashboardPod(t *testing.T) {
	rendered := helmRenderAll(t, "./sparkwing-full", "sparkwing", "default",
		"sparkwing-runner-bundle.controller.tokenSecret.name=tok")
	if !strings.Contains(rendered, "kind: NetworkPolicy") {
		t.Fatal("the flagship chart rendered no cache NetworkPolicy; the vendored sub-chart may be stale")
	}
	web := componentResource(t, renderedResources(t, rendered), "Deployment", "web")
	var policy renderedNetworkPolicy
	decoder := yaml.NewDecoder(strings.NewReader(rendered))
	for {
		var candidate renderedNetworkPolicy
		if err := decoder.Decode(&candidate); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("decode NetworkPolicy: %v", err)
		}
		if candidate.Kind == "NetworkPolicy" {
			policy = candidate
		}
	}
	if policy.Kind != "NetworkPolicy" {
		t.Fatal("no NetworkPolicy decoded from the flagship render")
	}
	for _, peer := range policy.Spec.Ingress[0].From {
		if selectorMatches(peer.PodSelector.MatchLabels, web.Spec.Template.Metadata.Labels) {
			return
		}
	}
	t.Errorf("no ingress peer selects the web pod labels %v", web.Spec.Template.Metadata.Labels)
}

func selectorMatches(selector, labels map[string]string) bool {
	if len(selector) == 0 {
		return false
	}
	for key, value := range selector {
		if labels[key] != value {
			return false
		}
	}
	return true
}

func TestCacheNetworkPolicyIsOptOut(t *testing.T) {
	rendered := helmRenderAll(t, "./sparkwing-runner-bundle", "sparkwing", "default", "networkPolicy.enabled=false")
	if strings.Contains(rendered, "kind: NetworkPolicy") {
		t.Error("networkPolicy.enabled=false still rendered a NetworkPolicy")
	}
	if !strings.Contains(helmRenderAll(t, "./sparkwing-runner-bundle", "sparkwing", "default"), "kind: NetworkPolicy") {
		t.Error("the default install rendered no cache NetworkPolicy")
	}
}

func TestPublishedCacheWithoutATokenFailsAtRender(t *testing.T) {
	for _, serviceType := range []string{"LoadBalancer", "NodePort"} {
		out := helmRenderError(t, "./sparkwing-runner-bundle", "sparkwing",
			"cache.service.type="+serviceType, "cache.allowUnauthenticated=true")
		for _, want := range []string{"cache.service.type=" + serviceType, "ClusterIP", "controller.tokenSecret.name"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s render error does not name %q:\n%s", serviceType, want, out)
			}
		}
	}
	rendered := helmRender(t, "./sparkwing-runner-bundle", "templates/cache-service.yaml", "sparkwing",
		"cache.service.type=LoadBalancer")
	if !strings.Contains(rendered, "type: LoadBalancer") {
		t.Errorf("a tokened cache refused a LoadBalancer Service:\n%s", rendered)
	}
}

func TestControllerCarriesTheCacheToken(t *testing.T) {
	controller := renderController(t, "sparkwing-runner-bundle.controller.tokenSecret.name=sparkwing-token",
		"sparkwing-runner-bundle.controller.tokenSecret.key=bearer")
	for _, env := range controller.Env {
		if env.Name != "SPARKWING_CACHE_TOKEN" {
			continue
		}
		if env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
			t.Fatalf("SPARKWING_CACHE_TOKEN is not a secretKeyRef: %+v", env)
		}
		ref := *env.ValueFrom.SecretKeyRef
		if ref.Name != "sparkwing-token" || ref.Key != "bearer" {
			t.Errorf("controller cache token = %+v, want the release token Secret", ref)
		}
		return
	}
	t.Fatalf("controller has no SPARKWING_CACHE_TOKEN env: %+v", controller.Env)
}

func TestControllerCarriesTheCacheURLBesideTheToken(t *testing.T) {
	controller := renderController(t, "sparkwing-runner-bundle.controller.tokenSecret.name=sparkwing-token")
	env := map[string]string{}
	var tokenRef *renderedSecretKeyRef
	for _, e := range controller.Env {
		env[e.Name] = e.Value
		if e.Name == "SPARKWING_CACHE_TOKEN" && e.ValueFrom != nil {
			tokenRef = e.ValueFrom.SecretKeyRef
		}
	}
	const want = "http://sparkwing-sparkwing-runner-bundle-cache.default.svc.cluster.local"
	if got := env["SPARKWING_CACHE_URL"]; got != want {
		t.Errorf("SPARKWING_CACHE_URL = %q, want %q", got, want)
	}
	if tokenRef == nil {
		t.Error("controller has no SPARKWING_CACHE_TOKEN secretKeyRef")
	}
}

func TestControllerCacheURLOverrideWins(t *testing.T) {
	controller := renderController(t, "sparkwing-runner-bundle.controller.tokenSecret.name=sparkwing-token",
		"controller.cache.url=http://cache.elsewhere:8090")
	for _, e := range controller.Env {
		if e.Name == "SPARKWING_CACHE_URL" && e.Value != "http://cache.elsewhere:8090" {
			t.Errorf("SPARKWING_CACHE_URL = %q, want the explicit override", e.Value)
		}
	}
}

func TestControllerHasNoCacheURLWhenNoCacheIsDeployed(t *testing.T) {
	controller := renderController(t, "sparkwing-runner-bundle.enabled=false")
	for _, e := range controller.Env {
		if e.Name == "SPARKWING_CACHE_URL" {
			t.Errorf("rendered SPARKWING_CACHE_URL=%q with no cache deployed", e.Value)
		}
	}
}

func hasEmptyDirVolume(volumes []renderedVolume, name string) bool {
	for _, volume := range volumes {
		if volume.Name == name && volume.EmptyDir != nil {
			return true
		}
	}
	return false
}

func hasMount(mounts []renderedVolumeMount, name, path string) bool {
	for _, mount := range mounts {
		if mount.Name == name && mount.MountPath == path {
			return true
		}
	}
	return false
}

func TestChartsDefaultToARestrictedRuntime(t *testing.T) {
	for _, test := range []struct {
		name     string
		chart    string
		template string
	}{
		{name: "controller", chart: "./sparkwing-full", template: "templates/controller-deployment.yaml"},
		{name: "web", chart: "./sparkwing-full", template: "templates/web-deployment.yaml"},
		{name: "runner", chart: "./sparkwing-runner-bundle", template: "templates/runner-deployment.yaml"},
		{name: "cache", chart: "./sparkwing-runner-bundle", template: "templates/cache-deployment.yaml"},
		{name: "logs", chart: "./sparkwing-runner-bundle", template: "templates/logs-deployment.yaml"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rendered := helmRender(t, test.chart, test.template, "sparkwing")
			doc := deploymentDocument(t, rendered)
			pod := doc.Spec.Template.Spec
			if pod.SecurityContext.SeccompProfile == nil || pod.SecurityContext.SeccompProfile.Type != "RuntimeDefault" {
				t.Fatalf("pod seccomp profile = %+v, want RuntimeDefault", pod.SecurityContext.SeccompProfile)
			}
			container := runnerContainer(t, rendered)
			if container.SecurityContext.ReadOnlyRootFilesystem == nil || !*container.SecurityContext.ReadOnlyRootFilesystem {
				t.Fatalf("readOnlyRootFilesystem = %+v, want true", container.SecurityContext.ReadOnlyRootFilesystem)
			}
			if !hasMount(container.VolumeMounts, "scratch", "/tmp") {
				t.Fatalf("volume mounts = %+v, want a scratch mount at /tmp", container.VolumeMounts)
			}
			if !hasEmptyDirVolume(pod.Volumes, "scratch") {
				t.Fatalf("volumes = %+v, want a scratch emptyDir", pod.Volumes)
			}
		})
	}
}

func TestPublishedDashboardWithoutTLSFailsAtRender(t *testing.T) {
	out := helmRenderError(t, "./sparkwing-full", "sparkwing",
		"ingress.enabled=true", "web.requireLogin=true")
	if !strings.Contains(out, "ingress.tls") || !strings.Contains(out, "ingress.allowInsecure=true") {
		t.Fatalf("render error does not name the TLS knob or its opt-out:\n%s", out)
	}
}

func TestPublishedDashboardWithoutLoginFailsAtRender(t *testing.T) {
	out := helmRenderError(t, "./sparkwing-full", "sparkwing",
		"ingress.enabled=true", "ingress.tls[0].secretName=sparkwing-tls")
	if !strings.Contains(out, "web.requireLogin") || !strings.Contains(out, "ingress.allowInsecure=true") {
		t.Fatalf("render error does not name the login knob or its opt-out:\n%s", out)
	}
}

func TestPublishedDashboardRendersOnceTLSAndLoginAreSet(t *testing.T) {
	rendered := helmRender(t, "./sparkwing-full", "templates/ingress.yaml", "sparkwing",
		"ingress.enabled=true", "web.requireLogin=true", "ingress.tls[0].secretName=sparkwing-tls")
	if !strings.Contains(rendered, "kind: Ingress") {
		t.Fatalf("no Ingress rendered:\n%s", rendered)
	}
}

func TestPublishedDashboardAcceptsAnExplicitInsecureOptIn(t *testing.T) {
	rendered := helmRender(t, "./sparkwing-full", "templates/ingress.yaml", "sparkwing",
		"ingress.enabled=true", "ingress.allowInsecure=true")
	if !strings.Contains(rendered, "kind: Ingress") {
		t.Fatalf("no Ingress rendered:\n%s", rendered)
	}
}

func TestStringInsecureOptInFailsAtRender(t *testing.T) {
	for _, value := range []string{"false", "true"} {
		t.Run(value, func(t *testing.T) {
			out := helmRenderErrorSetString(t, "./sparkwing-full", "sparkwing",
				"ingress.enabled=true", "ingress.allowInsecure="+value)
			if !strings.Contains(out, "ingress.allowInsecure must be a bool") {
				t.Fatalf("render error does not reject the quoted opt-out:\n%s", out)
			}
		})
	}
}

func TestInsecureOptInWithoutTLSAllowsSessionCookiesOverHTTP(t *testing.T) {
	container := runnerContainer(t, helmRender(t, "./sparkwing-full", "templates/web-deployment.yaml", "sparkwing",
		"ingress.enabled=true", "ingress.allowInsecure=true"))
	insecure := ""
	for _, env := range container.Env {
		if env.Name == "SPARKWING_WEB_INSECURE_COOKIES" {
			insecure = env.Value
		}
	}
	if insecure != "1" {
		t.Fatalf("SPARKWING_WEB_INSECURE_COOKIES = %q, want 1 so a browser can hold a session over plain HTTP", insecure)
	}

	secured := runnerContainer(t, helmRender(t, "./sparkwing-full", "templates/web-deployment.yaml", "sparkwing",
		"ingress.enabled=true", "web.requireLogin=true", "ingress.tls[0].secretName=sparkwing-tls"))
	for _, env := range secured.Env {
		if env.Name == "SPARKWING_WEB_INSECURE_COOKIES" {
			t.Fatalf("SPARKWING_WEB_INSECURE_COOKIES set behind TLS: %+v", env)
		}
	}
}

func TestPublishedDashboardAcceptsTLSWithoutASecretName(t *testing.T) {
	rendered := helmRender(t, "./sparkwing-full", "templates/ingress.yaml", "sparkwing",
		"ingress.enabled=true", "web.requireLogin=true",
		"ingress.tls[0].hosts[0]=sparkwing.example.com")
	if !strings.Contains(rendered, "kind: Ingress") {
		t.Fatalf("no Ingress rendered:\n%s", rendered)
	}
}

type renderedResourceGuard struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name      string `yaml:"name"`
		Namespace string `yaml:"namespace"`
	} `yaml:"metadata"`
	Spec struct {
		Hard   map[string]string `yaml:"hard"`
		Limits []struct {
			Type           string            `yaml:"type"`
			Max            map[string]string `yaml:"max"`
			Min            map[string]string `yaml:"min"`
			Default        map[string]string `yaml:"default"`
			DefaultRequest map[string]string `yaml:"defaultRequest"`
		} `yaml:"limits"`
	} `yaml:"spec"`
}

func renderResourceGuard(t *testing.T, showOnly string, sets ...string) renderedResourceGuard {
	t.Helper()
	rendered := helmRender(t, "./sparkwing-runner-bundle", showOnly, "sparkwing", sets...)
	var doc renderedResourceGuard
	decoder := yaml.NewDecoder(strings.NewReader(rendered))
	for {
		var candidate renderedResourceGuard
		if err := decoder.Decode(&candidate); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("decode %s: %v\n%s", showOnly, err, rendered)
		}
		if candidate.Kind != "" {
			doc = candidate
		}
	}
	if doc.Kind == "" {
		t.Fatalf("no resource guard rendered from %s:\n%s", showOnly, rendered)
	}
	return doc
}

func TestNamespaceResourceGuardsAreOffByDefault(t *testing.T) {
	rendered := helmRenderAll(t, "./sparkwing-runner-bundle", "sparkwing", "default")
	for _, kind := range []string{"LimitRange", "ResourceQuota"} {
		if strings.Contains(rendered, "kind: "+kind) {
			t.Errorf("default install renders a %s; an existing release would start failing admission", kind)
		}
	}
}

func TestLimitRangeBoundsOneContainer(t *testing.T) {
	doc := renderResourceGuard(t, "templates/limitrange.yaml", "limitRange.enabled=true")
	if doc.Kind != "LimitRange" {
		t.Fatalf("kind = %q, want LimitRange", doc.Kind)
	}
	if doc.Metadata.Name != "sparkwing-sparkwing-runner-bundle-limits" {
		t.Errorf("name = %q, want the release-scoped limits name", doc.Metadata.Name)
	}
	if len(doc.Spec.Limits) != 1 {
		t.Fatalf("limits = %d, want one Container entry", len(doc.Spec.Limits))
	}
	entry := doc.Spec.Limits[0]
	if entry.Type != "Container" {
		t.Errorf("type = %q, want Container", entry.Type)
	}
	want := map[string]string{"cpu": "16", "memory": "64Gi"}
	if !reflect.DeepEqual(entry.Max, want) {
		t.Errorf("max = %v, want %v", entry.Max, want)
	}
	if entry.Default["cpu"] != "2" || entry.DefaultRequest["cpu"] != "100m" {
		t.Errorf("default = %v, defaultRequest = %v, want the chart's container defaults", entry.Default, entry.DefaultRequest)
	}
	wantMin := map[string]string{"cpu": "10m", "memory": "16Mi"}
	if !reflect.DeepEqual(entry.Min, wantMin) {
		t.Errorf("min = %v, want the shipped floor %v", entry.Min, wantMin)
	}
}

func TestLimitRangeTakesAnOperatorCeiling(t *testing.T) {
	doc := renderResourceGuard(t, "templates/limitrange.yaml",
		"limitRange.enabled=true", "limitRange.max.cpu=4", "limitRange.max.memory=8Gi",
		"limitRange.min.cpu=50m")
	entry := doc.Spec.Limits[0]
	want := map[string]string{"cpu": "4", "memory": "8Gi"}
	if !reflect.DeepEqual(entry.Max, want) {
		t.Errorf("max = %v, want %v", entry.Max, want)
	}
	if entry.Min["cpu"] != "50m" {
		t.Errorf("min = %v, want the configured floor", entry.Min)
	}
}

func TestResourceQuotaBoundsTheNamespaceTotal(t *testing.T) {
	doc := renderResourceGuard(t, "templates/resourcequota.yaml",
		"resourceQuota.enabled=true", "limitRange.enabled=true")
	if doc.Kind != "ResourceQuota" {
		t.Fatalf("kind = %q, want ResourceQuota", doc.Kind)
	}
	if doc.Metadata.Name != "sparkwing-sparkwing-runner-bundle-quota" {
		t.Errorf("name = %q, want the release-scoped quota name", doc.Metadata.Name)
	}
	want := map[string]string{
		"requests.cpu":    "32",
		"requests.memory": "64Gi",
		"limits.cpu":      "64",
		"limits.memory":   "128Gi",
	}
	if !reflect.DeepEqual(doc.Spec.Hard, want) {
		t.Errorf("hard = %v, want %v", doc.Spec.Hard, want)
	}
}

func TestResourceQuotaTakesOperatorTotals(t *testing.T) {
	doc := renderResourceGuard(t, "templates/resourcequota.yaml",
		"resourceQuota.enabled=true", "limitRange.enabled=true",
		`resourceQuota.hard.requests\.cpu=8`, `resourceQuota.hard.pods=20`)
	if doc.Spec.Hard["requests.cpu"] != "8" || doc.Spec.Hard["pods"] != "20" {
		t.Errorf("hard = %v, want the configured totals including the object count", doc.Spec.Hard)
	}
}

func TestFullChartCarriesTheNamespaceResourceGuards(t *testing.T) {
	rendered := helmRenderAll(t, "./sparkwing-full", "sparkwing", "default",
		"sparkwing-runner-bundle.controller.tokenSecret.name=tok",
		"sparkwing-runner-bundle.limitRange.enabled=true",
		"sparkwing-runner-bundle.resourceQuota.enabled=true")
	for _, kind := range []string{"LimitRange", "ResourceQuota"} {
		if !strings.Contains(rendered, "kind: "+kind) {
			t.Errorf("flagship chart rendered no %s; the vendored sub-chart may be stale", kind)
		}
	}
}

func TestResourceQuotaWithoutALimitRangeFailsAtRender(t *testing.T) {
	out := helmRenderError(t, "./sparkwing-runner-bundle", "sparkwing", "resourceQuota.enabled=true")
	if !strings.Contains(out, "requires limitRange.enabled=true") {
		t.Fatalf("render error = %s, want the missing-LimitRange refusal", out)
	}
}

func TestRunnerCarriesNoJobCeilingByDefault(t *testing.T) {
	env := runnerEnv(t, renderRunner(t))
	for _, name := range []string{"SPARKWING_K8S_CPU_CEILING", "SPARKWING_K8S_MEMORY_CEILING"} {
		if _, ok := env[name]; ok {
			t.Errorf("default install sets %s; the ceiling is opt-in", name)
		}
	}
}

func TestRunnerCarriesTheConfiguredJobCeiling(t *testing.T) {
	env := runnerEnv(t, renderRunner(t, "runner.jobCeiling.cpu=8", "runner.jobCeiling.memory=16Gi"))
	if env["SPARKWING_K8S_CPU_CEILING"] != "8" || env["SPARKWING_K8S_MEMORY_CEILING"] != "16Gi" {
		t.Errorf("runner env = %v, want the configured job ceiling", env)
	}
}

func TestFullChartCarriesTheJobCeiling(t *testing.T) {
	rendered := helmRenderAll(t, "./sparkwing-full", "sparkwing", "default",
		"sparkwing-runner-bundle.controller.tokenSecret.name=tok",
		"sparkwing-runner-bundle.runner.jobCeiling.cpu=8")
	if !strings.Contains(rendered, "SPARKWING_K8S_CPU_CEILING") {
		t.Error("flagship chart carries no job ceiling env; the vendored sub-chart may be stale")
	}
}

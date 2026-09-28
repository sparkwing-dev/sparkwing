package launcher

import (
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/sparkwing-dev/sparkwing/internal/runners/k8s"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func testConfig() Config {
	return Config{
		Namespace: "sparkwing-jobs", ControllerURL: "http://controller",
		Image:      "registry/sparkwing-runner@sha256:" + strings.Repeat("a", 64),
		CPUCeiling: 8, MemoryCeiling: 16 << 30, Deadline: time.Hour,
	}
}

func testClaim() store.LaunchClaim {
	return store.LaunchClaim{
		Team: "alpha", RunID: "run-1", NodeID: "build", Generation: 1, Kind: store.ClaimTokenWork,
		Dispatch: store.RepoDispatchController, Class: store.CPUClass{Cores: 4, MemoryBytes: 14 << 30},
		Token: "swc_one",
	}
}

// Two claims that differ in every field a pipeline influences build the same
// Job once the claim's IDs, token and class are set aside: nothing else a
// plan declares reaches the spec.
func TestBuildJob_OnlyTheClaimsIDsTokenAndClassVary(t *testing.T) {
	cfg := testConfig()
	a, b := testClaim(), testClaim()
	b.Team, b.RunID, b.NodeID, b.Generation, b.Kind, b.Token = "bravo", "run-2", "deploy", 7, store.ClaimTokenPlan, "swc_two"
	b.Class = store.CPUClass{Cores: 64, MemoryBytes: 1 << 40}
	ja, jb := BuildJob(cfg, a), BuildJob(cfg, b)
	pa, pb := ja.Spec.Template.Spec, jb.Spec.Template.Spec
	ca, cb := pa.Containers[0], pb.Containers[0]
	pa.Containers, pb.Containers, pa.NodeSelector, pb.NodeSelector = nil, nil, nil, nil
	if !reflect.DeepEqual(pa, pb) || !reflect.DeepEqual(ja.Spec.ActiveDeadlineSeconds, jb.Spec.ActiveDeadlineSeconds) {
		t.Fatalf("pod specs differ beyond the claim:\n%+v\n%+v", pa, pb)
	}
	if ca.Image != cb.Image || !reflect.DeepEqual(ca.Command, cb.Command) || !reflect.DeepEqual(ca.SecurityContext, cb.SecurityContext) ||
		!reflect.DeepEqual(ca.VolumeMounts, cb.VolumeMounts) {
		t.Fatalf("containers differ beyond the claim:\n%+v\n%+v", ca, cb)
	}
	if got := cb.Args; !reflect.DeepEqual(got, []string{"run-node", "run-2", "deploy"}) {
		t.Fatalf("args = %v", got)
	}
	cpu, mem := cb.Resources.Limits[corev1.ResourceCPU], cb.Resources.Limits[corev1.ResourceMemory]
	if cpu.Cmp(resource.MustParse("8")) != 0 || mem.Value() != cfg.MemoryCeiling {
		t.Fatalf("a 64-core class was not held to the ceilings: cpu %s memory %s", cpu.String(), mem.String())
	}
	if !reflect.DeepEqual(cb.Resources.Requests, cb.Resources.Limits) {
		t.Fatalf("requests %v differ from limits %v", cb.Resources.Requests, cb.Resources.Limits)
	}
}

func TestBuildJob_PinsIdentityVolumesAndPlacement(t *testing.T) {
	job := BuildJob(testConfig(), testClaim())
	pod := job.Spec.Template.Spec
	c := pod.Containers[0]
	if pod.ServiceAccountName != ServiceAccount || *pod.AutomountServiceAccountToken || *pod.EnableServiceLinks ||
		pod.HostNetwork || pod.HostPID || pod.HostIPC {
		t.Fatalf("pod identity: %+v", pod)
	}
	if *job.Spec.BackoffLimit != 0 || *job.Spec.ActiveDeadlineSeconds != int64(time.Hour.Seconds()) {
		t.Fatalf("backoff %d deadline %d", *job.Spec.BackoffLimit, *job.Spec.ActiveDeadlineSeconds)
	}
	for _, v := range pod.Volumes {
		if v.EmptyDir == nil || v.EmptyDir.SizeLimit == nil {
			t.Fatalf("volume %s is not a size-limited emptyDir", v.Name)
		}
	}
	var credentials []string
	for _, e := range c.Env {
		if e.ValueFrom != nil {
			t.Fatalf("env %s reads from a reference", e.Name)
		}
		if strings.HasPrefix(e.Value, store.ClaimTokenPrefix+"_") {
			credentials = append(credentials, e.Name)
		}
	}
	if !reflect.DeepEqual(credentials, []string{"SPARKWING_AGENT_TOKEN"}) {
		t.Fatalf("credential env = %v, want the claim token alone", credentials)
	}
	if pod.NodeSelector[k8s.CPUBandKey] != k8s.CPUBandSmall || pod.NodeSelector[k8s.TeamNodeLabel] != "alpha" {
		t.Fatalf("node selector = %v", pod.NodeSelector)
	}
	if !*c.SecurityContext.ReadOnlyRootFilesystem || *c.SecurityContext.AllowPrivilegeEscalation ||
		len(c.SecurityContext.Capabilities.Drop) != 1 || !*pod.SecurityContext.RunAsNonRoot {
		t.Fatalf("security context: %+v %+v", c.SecurityContext, pod.SecurityContext)
	}
	term := pod.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0]
	if term.LabelSelector.MatchExpressions[0].Key != JobLabel || job.Spec.Template.Labels[JobLabel] != job.Name {
		t.Fatalf("anti-affinity %+v labels %v", term, job.Spec.Template.Labels)
	}
}

func TestConfigValidate_RefusesAnUnpinnedOrUncappedLauncher(t *testing.T) {
	if err := testConfig().Validate(); err != nil {
		t.Fatalf("valid config refused: %v", err)
	}
	for name, mutate := range map[string]func(*Config){
		"tag image":      func(c *Config) { c.Image = "registry/sparkwing-runner:latest" },
		"no cpu ceiling": func(c *Config) { c.CPUCeiling = 0 },
		"no mem ceiling": func(c *Config) { c.MemoryCeiling = 0 },
		"past 6h":        func(c *Config) { c.Deadline = 7 * time.Hour },
		"bad scratch":    func(c *Config) { c.ScratchLimit = "lots" },
	} {
		cfg := testConfig()
		mutate(&cfg)
		if cfg.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

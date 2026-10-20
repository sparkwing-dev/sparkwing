package acceptance_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

type cloudBoundaryFake struct {
	missingNetwork bool
	wrongTeam      bool
	reserved       int
	released       int
}

func (b *cloudBoundaryFake) Reserve(_ context.Context, binding launcher.CloudJobBinding, name string) (launcher.CloudNodeReservation, launcher.CloudNetworkEvidence, error) {
	b.reserved++
	team := binding.Team
	if b.wrongTeam {
		team = "other-team"
	}
	return launcher.CloudNodeReservation{NodeName: "node", NodeUID: "uid", Team: team, JobName: name, ReservationID: "reservation", Award: binding.LeaseRef()}, launcher.CloudNetworkEvidence{Namespace: "cloud", NodeUID: "uid", PolicyRevision: "revision", DefaultDenyEgressEnforced: !b.missingNetwork, RequiredDestinationsAllowed: true, DaemonSetPortsInventoried: true, EscapeProbePassed: true}, nil
}

func (b *cloudBoundaryFake) Release(context.Context, launcher.CloudNodeReservation) error {
	b.released++
	return nil
}

func cloudFixture(kind launcher.CloudUnitKind) (time.Time, launcher.CloudJobConfig, launcher.CloudJobAward) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	image := "registry/runner@sha256:" + strings.Repeat("a", 64)
	cfg := launcher.CloudJobConfig{Namespace: "cloud", RunnerImage: image, DockerImage: image, ControllerURL: "https://controller.example", CPUCeiling: 4, MemoryCeiling: 8 << 30}
	award := launcher.CloudJobAward{LeaseExpiresAt: now.Add(10 * time.Minute), Binding: launcher.CloudJobBinding{UnitID: "unit", Kind: kind, Team: "team", RunID: "run", NodeID: "job", Holder: "holder", Generation: 1, Attempt: 1, OwnerDeadline: now.Add(time.Hour), AttemptDeadline: now.Add(10 * time.Minute), ParentCapabilityExpiry: now.Add(45 * time.Minute)}, Bootstrap: []byte("secret-bootstrap-value"), CPUCores: 1.5, Memory: 2 << 30}
	if kind != launcher.CloudPlan {
		award.Engine = &engine.Identity{Pin: engine.Pin{SDK: engine.GoSDK, Version: "v1.2.3", InputsDigest: strings.Repeat("c", 64)}, Version: "v1.2.3", ManifestDigest: strings.Repeat("b", 64)}
	} else {
		award.Binding.NodeID, award.Binding.Attempt = "", 0
	}
	if kind == launcher.CloudEval {
		award.Binding.SourceNodeID = award.Binding.NodeID
		award.Binding.SourceGeneration = 2
	}
	return now, cfg, award
}

func FuzzCloudAwardUnitBindingW8(f *testing.F) {
	f.Add(uint8(0), "", "", int64(0))
	f.Add(uint8(0), "job", "", int64(0))
	f.Add(uint8(0), "", "source", int64(1))
	f.Add(uint8(1), "job", "job", int64(2))
	f.Add(uint8(1), "job", "other", int64(2))
	f.Add(uint8(1), "", "", int64(1))
	f.Add(uint8(1), "job", "job", int64(0))
	f.Add(uint8(1), "job", "job", int64(-1))
	f.Add(uint8(2), "job", "", int64(0))
	f.Add(uint8(2), "job", "source", int64(2))
	f.Add(uint8(2), "", "", int64(0))
	f.Add(uint8(2), "\xe6", "", int64(0))
	f.Fuzz(func(t *testing.T, kind uint8, job, source string, generation int64) {
		job, source = strings.ToValidUTF8(job, "\ufffd"), strings.ToValidUTF8(source, "\ufffd")
		kinds := []launcher.CloudUnitKind{launcher.CloudPlan, launcher.CloudEval, launcher.CloudWork}
		now, cfg, award := cloudFixture(kinds[kind%3])
		award.Binding.NodeID, award.Binding.SourceNodeID, award.Binding.SourceGeneration = job, source, generation
		valid := false
		switch award.Binding.Kind {
		case launcher.CloudPlan:
			valid = job == "" && source == "" && generation == 0
		case launcher.CloudEval:
			valid = job != "" && source == job && generation >= 0
		case launcher.CloudWork:
			valid = job != "" && source == "" && generation == 0
		}
		boundary := &cloudBoundaryFake{}
		resources, err := launcher.BuildJob(t.Context(), now, cfg, award, boundary)
		if (err == nil) != valid {
			t.Fatalf("unit binding: kind=%s job=%q source=%q generation=%d valid=%t error=%v", award.Binding.Kind, job, source, generation, valid, err)
		}
		if !valid && (resources.Job != nil || resources.BootstrapSecret != nil || boundary.reserved != 0) {
			t.Fatal("invalid unit binding produced Cloud resources")
		}
		metadata, err := json.Marshal(award)
		if err != nil {
			t.Fatal(err)
		}
		file := writeBootstrapW8(t, map[string][]byte{"bootstrap": award.Bootstrap, "award.json": metadata})
		restored, err := launcher.ReadBootstrapAward(file)
		if (err == nil) != valid || (valid && restored.Binding != award.Binding) || (!valid && len(restored.Bootstrap) != 0) {
			t.Fatalf("private unit binding: valid=%t error=%v", valid, err)
		}
	})
}

func TestLauncherCloudPodContract(t *testing.T) {
	for _, kind := range []launcher.CloudUnitKind{launcher.CloudPlan, launcher.CloudEval, launcher.CloudWork} {
		t.Run(string(kind), func(t *testing.T) {
			now, cfg, award := cloudFixture(kind)
			resources, err := launcher.BuildJob(t.Context(), now, cfg, award, &cloudBoundaryFake{})
			if err != nil {
				t.Fatal(err)
			}
			pod := resources.Job.Spec.Template.Spec
			if pod.HostUsers == nil || *pod.HostUsers || pod.SecurityContext == nil || pod.SecurityContext.RunAsUser == nil || *pod.SecurityContext.RunAsUser != 0 {
				t.Fatal("Cloud Job is not root of its own user namespace")
			}
			if pod.NodeName != "node" || pod.ServiceAccountName != launcher.ServiceAccount || pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken || pod.HostNetwork || pod.HostPID || pod.HostIPC {
				t.Fatal("Cloud Job lost its placement or host boundary")
			}
			if len(pod.InitContainers) != 2 || pod.InitContainers[0].Name != "docker" || pod.InitContainers[0].RestartPolicy == nil || *pod.InitContainers[0].RestartPolicy != corev1.ContainerRestartPolicyAlways || pod.InitContainers[0].SecurityContext.Privileged != nil {
				t.Fatal("Docker is not an unprivileged native sidecar")
			}
			if pod.InitContainers[1].Name != "fetch-source" || len(pod.Containers) != 1 {
				t.Fatal("source must finish before the host")
			}
			var scratch int64
			for _, volume := range pod.Volumes {
				if volume.HostPath != nil {
					t.Fatal("hostPath exposed")
				}
				if volume.EmptyDir != nil {
					if volume.EmptyDir.SizeLimit == nil {
						t.Fatal("unbounded scratch")
					}
					scratch += volume.EmptyDir.SizeLimit.Value()
				}
			}
			if scratch != limits.JobScratchBytes {
				t.Fatalf("scratch = %d", scratch)
			}
			for _, mount := range pod.InitContainers[0].VolumeMounts {
				if mount.Name == "bootstrap" {
					t.Fatal("Docker can read bootstrap mount")
				}
			}
			var modes []int32
			for _, volume := range pod.Volumes {
				if volume.Secret != nil {
					modes = append(modes, *volume.Secret.DefaultMode)
				}
			}
			if len(modes) != 1 || modes[0] != 0o400 {
				t.Fatalf("bootstrap modes = %v", modes)
			}
			raw, err := json.Marshal(resources.Job)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), string(award.Bootstrap)) || strings.Contains(string(raw), "SPARKWING_AGENT_TOKEN") {
				t.Fatal("credential leaked to Job")
			}
			if string(resources.BootstrapSecret.Data["bootstrap"]) != string(award.Bootstrap) {
				t.Fatal("missing bootstrap")
			}
			want := award.LeaseExpiresAt
			if resources.JobDeadline != want {
				t.Fatalf("deadline = %v", resources.JobDeadline)
			}
			args := strings.Join(pod.Containers[0].Args, " ")
			for _, flag := range []string{"--controller", "--run", "--generation", "--source-dir", "--module-cache", "--bootstrap-file"} {
				if !strings.Contains(args, flag) {
					t.Fatalf("missing %s", flag)
				}
			}
			if strings.Contains(args, "--job") != (kind != launcher.CloudPlan) {
				t.Fatal("job argument does not match the operational unit")
			}
		})
	}
}

func TestLauncherCloudBoundaryRefusal(t *testing.T) {
	for _, name := range []string{"network", "team", "expired", "engine", "cpu", "memory", "cpu_precision"} {
		t.Run(name, func(t *testing.T) {
			now, cfg, award := cloudFixture(launcher.CloudWork)
			boundary := &cloudBoundaryFake{}
			switch name {
			case "network":
				boundary.missingNetwork = true
			case "team":
				boundary.wrongTeam = true
			case "expired":
				award.Binding.ParentCapabilityExpiry = now
			case "engine":
				award.Engine = nil
			case "cpu":
				award.CPUCores = 4.5
			case "memory":
				award.Memory = 9 << 30
			case "cpu_precision":
				award.CPUCores, cfg.CPUCeiling = 3.9995, 3.9995
			}
			resources, err := launcher.BuildJob(t.Context(), now, cfg, award, boundary)
			if err == nil || resources.Job != nil || resources.BootstrapSecret != nil {
				t.Fatal("refused launch produced runnable objects")
			}
			if (name == "network" || name == "team") && boundary.released != 1 {
				t.Fatal("rejected reservation leaked")
			}
			if name == "expired" && !errors.Is(err, limits.ErrNoTime) {
				t.Fatalf("expiry error = %v", err)
			}
		})
	}
}

func TestLauncherCloudPlanUsesRunBinding(t *testing.T) {
	now, cfg, award := cloudFixture(launcher.CloudPlan)
	award.Binding.NodeID, award.Binding.Attempt = "", 0
	award.Binding.Attempt = 0
	r, err := launcher.BuildJob(t.Context(), now, cfg, award, &cloudBoundaryFake{})
	if err != nil {
		t.Fatal(err)
	}
	for _, container := range append(r.Job.Spec.Template.Spec.InitContainers, r.Job.Spec.Template.Spec.Containers...) {
		for _, arg := range container.Args {
			if arg == "--job" {
				t.Fatal("plan acquired a fabricated job binding")
			}
		}
	}
}

func (b *cloudBoundaryFake) Retire(context.Context, launcher.CloudNodeReservation) error { return nil }

func cloudWireAwardW8(award launcher.CloudJobAward) controllerwire.ClaimedUnit {
	b := award.Binding
	unit := controllerwire.ClaimedUnit{LeaseExpiresAt: award.LeaseExpiresAt, Unit: capability.UnitNode, Purpose: capability.PurposeWork, Delivery: capability.DeliveryBootstrap, UnitID: b.UnitID, RunID: b.RunID, JobID: b.NodeID, Binding: capability.Binding{Kind: capability.BindingNode, RunID: b.RunID, NodeID: b.NodeID, Holder: b.Holder, Generation: b.Generation, Attempt: b.Attempt}, Request: runspec.SealedRequest{RunID: b.RunID, Request: runspec.Request{Team: b.Team}}, OwnerDeadline: &b.OwnerDeadline, AttemptDeadline: &b.AttemptDeadline, ParentCapabilityExpiry: &b.ParentCapabilityExpiry, Engine: award.Engine, Execution: &runspec.JobExecutionSettings{}, CloudResources: &match.Resources{Cores: award.CPUCores, MemoryBytes: award.Memory}}
	if award.Engine != nil {
		unit.Accepted = &controllerwire.PlanAccepted{Spec: runspec.Spec{Request: unit.Request, Resolved: runspec.Resolved{Engine: *award.Engine}}}
	}
	switch b.Kind {
	case launcher.CloudPlan:
		unit.Unit, unit.Purpose, unit.Binding.Kind = capability.UnitPlan, capability.PurposePlan, capability.BindingRun
	case launcher.CloudEval:
		unit.Unit, unit.Purpose, unit.SourceGeneration = capability.UnitEval, capability.PurposeEval, b.SourceGeneration
		unit.Binding.Generation = b.SourceGeneration
		unit.Binding.Expansion = &capability.LeaseRef{Kind: capability.UnitEval, ID: b.UnitID, Holder: b.Holder, Generation: b.Generation}
	}
	return unit
}

package executionpolicy

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/buildinfo"
)

func TestRuntimeCompatibilityRefusalOrderAndBuildIdentityIsObservational(t *testing.T) {
	policy := testExecutionPolicy()
	report := CurrentRuntimeReport(buildinfo.Identity{
		Binary: "sparkwing-runner", Version: "v999.0.0", Commit: "different-build",
		GOOS: "windows", GOARCH: "amd64",
	})
	report.Supervisor = append(report.Supervisor, "process-tree-v1")
	report.BodyHost = append(report.BodyHost, "actions-v1")

	if err := CheckRuntimeCompatibility(policy, report); err != nil {
		t.Fatalf("observational build identity affected admission: %v", err)
	}

	withoutSupervisor := report
	withoutSupervisor.Supervisor = nil
	basePolicy := policy
	basePolicy.SupervisorRequirements = []string{FleetSupervisorRequirement}
	var upgrade *UpgradeRequiredError
	if err := CheckRuntimeCompatibility(basePolicy, withoutSupervisor); !errors.As(err, &upgrade) ||
		upgrade.Scope != "supervisor" || !reflect.DeepEqual(upgrade.Missing, basePolicy.SupervisorRequirements) ||
		upgrade.MinimumRelease != "v0.41.0" || upgrade.SafeHold {
		t.Fatalf("missing supervisor result = %#v, %v", upgrade, err)
	}

	newerOnly := report
	newerOnly.BodyProtocolMinimum = policy.Body.ProtocolVersion + 1
	newerOnly.BodyProtocolMaximum = newerOnly.BodyProtocolMinimum
	var incompatible *ProtocolIncompatibleError
	if err := CheckRuntimeCompatibility(policy, newerOnly); !errors.As(err, &incompatible) ||
		incompatible.PolicyProtocol != policy.Body.ProtocolVersion {
		t.Fatalf("newer-only result = %#v, %v", incompatible, err)
	}

	withoutBodyHost := report
	withoutBodyHost.BodyHost = nil
	upgrade = nil
	if err := CheckRuntimeCompatibility(policy, withoutBodyHost); !errors.As(err, &upgrade) ||
		upgrade.Scope != "body_host" || len(upgrade.Missing) != 2 ||
		!containsRuntimeRequirement(upgrade.Missing, FleetBodyRequirement) ||
		!containsRuntimeRequirement(upgrade.Missing, "actions-v1") ||
		upgrade.MinimumRelease != "" || !upgrade.SafeHold {
		t.Fatalf("missing body-host result = %#v, %v", upgrade, err)
	}
}

func TestRuntimeUpgradeRegistryHoldsUnknownFloorsWithoutInventingRequirements(t *testing.T) {
	policy := testExecutionPolicy()
	report := CurrentRuntimeReport(buildinfo.Identity{})

	protocolOnly := policy
	protocolOnly.SupervisorRequirements = nil
	report.Supervisor = nil
	report.BodyProtocolMinimum = 0
	report.BodyProtocolMaximum = 0
	var upgrade *UpgradeRequiredError
	if err := CheckRuntimeCompatibility(protocolOnly, report); !errors.As(err, &upgrade) ||
		len(upgrade.Missing) != 0 || upgrade.MinimumRelease != "v0.41.0" || upgrade.SafeHold {
		t.Fatalf("known protocol floor = %#v, %v", upgrade, err)
	}

	unknownRequirement := policy
	unknownRequirement.SupervisorRequirements = []string{"fleet-supervisor-v1", "future-supervisor-v9"}
	upgrade = nil
	if err := CheckRuntimeCompatibility(unknownRequirement, report); !errors.As(err, &upgrade) ||
		upgrade.MinimumRelease != "" || !upgrade.SafeHold ||
		!reflect.DeepEqual(upgrade.Missing, unknownRequirement.SupervisorRequirements) {
		t.Fatalf("unknown requirement floor = %#v, %v", upgrade, err)
	}

	unknownProtocol := protocolOnly
	unknownProtocol.Body.ProtocolVersion = AssistedBodyProtocolVersion + 1
	report.BodyProtocolMaximum = AssistedBodyProtocolVersion
	upgrade = nil
	if err := CheckRuntimeCompatibility(unknownProtocol, report); !errors.As(err, &upgrade) ||
		upgrade.MinimumRelease != "" || !upgrade.SafeHold || len(upgrade.Missing) != 0 {
		t.Fatalf("unknown protocol floor = %#v, %v", upgrade, err)
	}
}

func TestRuntimeContextsAreRequestScopedAndBindingCodecIsStrict(t *testing.T) {
	report := CurrentRuntimeReport(buildinfo.Identity{Binary: "runner", Version: "v0.41.0"})
	ctx, err := WithRuntimeReport(context.Background(), report)
	if err != nil {
		t.Fatal(err)
	}
	gotReport, ok := RuntimeReportFromContext(ctx)
	if !ok || !reflect.DeepEqual(gotReport, report) {
		t.Fatalf("runtime report = (%+v, %v), want %+v", gotReport, ok, report)
	}
	if _, ok := RuntimeReportFromContext(context.Background()); ok {
		t.Fatal("runtime report escaped its request context")
	}

	binding := ClaimBinding{
		RunID: "run", NodeID: "node", PolicyHash: "sha256:policy", PolicyVersion: 1,
		BodyProtocol: 1, SupervisorRequirementsHash: "sha256:supervisor", BodyRequirementsHash: "sha256:body",
	}
	raw, err := EncodeClaimBinding(binding)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeClaimBinding(raw)
	if err != nil || decoded != binding {
		t.Fatalf("binding round trip = (%+v, %v)", decoded, err)
	}
	for name, candidate := range map[string][]byte{
		"unknown":  append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"secret":"sentinel"}`)...),
		"trailing": append(append([]byte(nil), raw...), []byte(` {}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeClaimBinding(candidate); !errors.Is(err, ErrExecutionPolicyInvalid) {
				t.Fatalf("DecodeClaimBinding error = %v, want invalid", err)
			}
		})
	}

	sink := NewPreparationSink()
	if err := StorePreparation(WithPreparationSink(context.Background(), sink), binding); err != nil {
		t.Fatal(err)
	}
	if got := sink.Load(); got != binding {
		t.Fatalf("sink binding = %+v, want %+v", got, binding)
	}
	if err := StorePreparation(context.Background(), binding); err == nil {
		t.Fatal("missing request-scoped sink accepted a private binding")
	}
}

func TestRuntimeReportRejectsUnboundedProtocolRange(t *testing.T) {
	report := CurrentRuntimeReport(buildinfo.Identity{})
	report.BodyProtocolMaximum = maxRuntimeProtocol + 1
	if _, err := NormalizeRuntimeReport(report); err == nil {
		t.Fatal("unbounded protocol range accepted")
	}
}

func containsRuntimeRequirement(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

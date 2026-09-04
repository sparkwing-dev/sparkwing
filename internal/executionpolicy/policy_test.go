package executionpolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func testExecutionPolicy() NodeExecutionPolicy {
	digest := "sha256:" + strings.Repeat("a", 64)
	return NodeExecutionPolicy{
		Version: NodeExecutionPolicyVersion, Pipeline: "release", NodeID: "deploy", AllowedLocations: []string{"cloud"},
		Dependencies:           []NodeDependencyAuthority{{NodeID: "build", WholeOutput: true, MaxBytes: 1 << 20}},
		SecretNames:            []string{"DEPLOY_TOKEN"},
		ArtifactInputs:         []NodeArtifactInput{{ProducerNodeID: "build", ManifestDigest: digest, Into: "artifacts/build", MaxBytes: 1 << 30, MaxFiles: 1000, MaxManifestBytes: 1 << 20}},
		ArtifactOutputs:        []NodeArtifactOutput{{Glob: "reports/report-*", MaxBytes: 1 << 20, MaxFiles: 100}},
		ArtifactOutputMaxBytes: 1 << 20,
		ResultMaxBytes:         1 << 20,
		Emission: NodeEmissionAuthority{
			Events:  []NodeExecutionEventKind{NodeExecutionEventSummary, NodeExecutionEventAnnotation},
			StepIDs: []string{"publish"}, MaxEvents: 100, MaxPayloadBytes: 1 << 16, MaxTotalBytes: 1 << 20,
		},
		SupervisorRequirements: []string{fleetSupervisorRuntimeRequirement, "process-tree-v1"},
		Body: NodeCompiledBodyAuthority{
			ProtocolVersion: AssistedBodyProtocolVersion, RuntimeRequirements: []string{fleetBodyRuntimeRequirement, "actions-v1"},
			Source: NodeBodySourceAuthority{Kind: "git", Identity: strings.Repeat("b", 40), ManifestDigest: digest, PlanDigest: digest},
		},
		Actions: []NodeExecutionGrant{
			{GrantID: "spawn-release", Kind: NodeExecutionActionSpawn, Spawn: &NodeSpawnGrant{SpawnID: "notify", TargetWorkIdentity: digest, MaxChildren: 1}},
			{GrantID: "tool-docker", Kind: NodeExecutionActionToolSlot, ToolSlot: &NodeToolSlotGrant{Group: "docker", Scope: "box", Capacity: 100, OnLimit: "queue", MaxCost: 1}},
		},
	}
}

func TestExecutionPolicyV1DoesNotPromiseUnimplementedOutputProjection(t *testing.T) {
	sealed, err := SealNew(testExecutionPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed.Canonical), "output_selectors") {
		t.Fatalf("v1 policy exposes an unenforced output projection: %s", sealed.Canonical)
	}
}

func TestExecutionPolicyCanonicalSealIsStable(t *testing.T) {
	policy := testExecutionPolicy()
	first, err := SealNew(policy)
	if err != nil {
		t.Fatal(err)
	}
	policy.SecretNames = []string{"DEPLOY_TOKEN"}
	policy.Emission.Events = []NodeExecutionEventKind{NodeExecutionEventAnnotation, NodeExecutionEventSummary}
	policy.Actions[0], policy.Actions[1] = policy.Actions[1], policy.Actions[0]
	second, err := SealNew(policy)
	if err != nil {
		t.Fatal(err)
	}
	if first.Hash != second.Hash || string(first.Canonical) != string(second.Canonical) {
		t.Fatalf("canonical seal changed across equivalent orderings:\n%s\n%s", first.Canonical, second.Canonical)
	}
	if strings.Contains(string(first.Canonical), "secret-value") {
		t.Fatal("canonical policy carried a secret value")
	}
}

func TestExecutionPolicyRejectsInexactIdentitiesAndOpenUnion(t *testing.T) {
	for name, mutate := range map[string]func(*NodeExecutionPolicy){
		"whitespace":            func(p *NodeExecutionPolicy) { p.NodeID = " deploy" },
		"control":               func(p *NodeExecutionPolicy) { p.SecretNames = []string{"TOKEN\n"} },
		"invalid utf8":          func(p *NodeExecutionPolicy) { p.Pipeline = string([]byte{0xff}) },
		"coordinator placement": func(p *NodeExecutionPolicy) { p.AllowedLocations = []string{"coordinator"} },
		"secret-shaped extension impossible": func(p *NodeExecutionPolicy) {
			p.Actions[0].Await = &NodeAwaitGrant{Pipeline: "steal"}
		},
		"absolute output":     func(p *NodeExecutionPolicy) { p.ArtifactOutputs[0].Glob = "/tmp/**" },
		"traversal output":    func(p *NodeExecutionPolicy) { p.ArtifactOutputs[0].Glob = "../**" },
		"noncanonical digest": func(p *NodeExecutionPolicy) { p.Body.Source.ManifestDigest = strings.Repeat("a", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			policy := testExecutionPolicy()
			mutate(&policy)
			if _, err := SealNew(policy); !errors.Is(err, ErrExecutionPolicyInvalid) {
				t.Fatalf("seal error = %v, want ErrExecutionPolicyInvalid", err)
			}
		})
	}
}

func TestExecutionPolicyDecodeRejectsUnknownAndTrailingJSON(t *testing.T) {
	policy := testExecutionPolicy()
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	for name, candidate := range map[string][]byte{
		"unknown":   append(raw[:len(raw)-1], []byte(`,"secret_value":"do-not-store"}`)...),
		"second":    append(raw, []byte(` {}`)...),
		"garbage":   append(raw, []byte(` nope`)...),
		"oversized": make([]byte, maxExecutionPolicyBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(candidate); !errors.Is(err, ErrExecutionPolicyInvalid) {
				t.Fatalf("decode error = %v, want ErrExecutionPolicyInvalid", err)
			}
		})
	}
}

func TestExecutionPolicyCarrierRejectsUnknownTrailingAndOwnsBytes(t *testing.T) {
	var owner Carrier
	if err := SetNew(&owner, testExecutionPolicy()); err != nil {
		t.Fatal(err)
	}
	raw, err := EncodeCarrier(&owner)
	if err != nil {
		t.Fatal(err)
	}
	unknown := append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"sentinel_private":"reject"}`)...)
	for name, candidate := range map[string][]byte{
		"unknown":  unknown,
		"trailing": append(append([]byte(nil), raw...), []byte(` {}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			var decoded Carrier
			if err := DecodeCarrierStrict(candidate, &decoded); !errors.Is(err, ErrExecutionPolicyInvalid) {
				t.Fatalf("decode %s carrier = %v, want invalid", name, err)
			}
			if IsSealed(&decoded) {
				t.Fatalf("decode %s partially changed owner", name)
			}
		})
	}

	raw[0] ^= 0xff
	again, err := EncodeCarrier(&owner)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(raw, again) {
		t.Fatal("encoded carrier aliases its owned bytes")
	}
	var copied Carrier
	CopyCarrier(&copied, &owner)
	ClearCarrier(&owner)
	if !IsSealed(&copied) {
		t.Fatal("carrier copy aliased cleared source")
	}
}

func TestExecutionPolicyDecodeDispatchesPersistedVersions(t *testing.T) {
	for name, mutate := range map[string]func(*NodeExecutionPolicy){
		"future policy": func(p *NodeExecutionPolicy) { p.Version = NodeExecutionPolicyVersion + 1 },
		"future body":   func(p *NodeExecutionPolicy) { p.Body.ProtocolVersion = AssistedBodyProtocolVersion + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			policy := testExecutionPolicy()
			mutate(&policy)
			raw, err := json.Marshal(policy)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Decode(raw); !errors.Is(err, ErrExecutionUpgradeRequired) {
				t.Fatalf("decode error = %v, want upgrade required", err)
			}
			if _, err := SealNew(policy); !errors.Is(err, ErrExecutionPolicyInvalid) {
				t.Fatalf("new seal error = %v, want invalid", err)
			}
		})
	}
}

func TestExecutionPolicyRejectsNonPortableWindowsPaths(t *testing.T) {
	for _, candidate := range []string{
		`C:relative`, `C:/absolute`, `file:stream`, `\\server\\share`, `\\?\\C:\\extended`,
		`CON`, `con.txt`, `dir/NUL`, `dir/com1.log`, `dir/clock$.log`, `dir/CONIN$.data`,
		`dir/COM¹.log`, `dir/trailing.`, `dir/trailing `, `dir/illegal<name`, `dir/question?`,
		`dir\\mixed/path`, `../escape`, `/absolute`,
	} {
		t.Run(candidate, func(t *testing.T) {
			policy := testExecutionPolicy()
			policy.ArtifactInputs[0].Into = candidate
			if _, err := SealNew(policy); !errors.Is(err, ErrExecutionPolicyInvalid) {
				t.Fatalf("path %q error = %v, want invalid", candidate, err)
			}
		})
	}
	for _, candidate := range []string{
		`reports/CON`, `reports/con.log`, `reports/NUL.txt`, `reports/C?N.log`, `reports/[cC][oO][nN].bin`,
		`reports/CON[.]txt`, `reports/NUL[.]*`, `reports/[cC][oO][nN][.]LoG`, `reports/nul[.][bB][iI][nN]`,
		`reports/file:stream`, `reports/trailing.`, `C:/**`,
	} {
		t.Run("glob/"+candidate, func(t *testing.T) {
			policy := testExecutionPolicy()
			policy.ArtifactOutputs[0].Glob = candidate
			if _, err := SealNew(policy); !errors.Is(err, ErrExecutionPolicyInvalid) {
				t.Fatalf("glob %q error = %v, want invalid", candidate, err)
			}
		})
	}
	t.Run("case-insensitive destinations", func(t *testing.T) {
		policy := testExecutionPolicy()
		other := policy.ArtifactInputs[0]
		other.ProducerNodeID = "other"
		other.Into = "ARTIFACTS/BUILD"
		policy.ArtifactInputs = append(policy.ArtifactInputs, other)
		if _, err := SealNew(policy); !errors.Is(err, ErrExecutionPolicyInvalid) {
			t.Fatalf("case-folded destination collision = %v, want invalid", err)
		}
	})
	t.Run("non-adjacent case-insensitive globs", func(t *testing.T) {
		policy := testExecutionPolicy()
		policy.ArtifactOutputs = []NodeArtifactOutput{
			{Glob: "A/report-*", MaxBytes: 1, MaxFiles: 1},
			{Glob: "B/report-*", MaxBytes: 1, MaxFiles: 1},
			{Glob: "a/report-*", MaxBytes: 1, MaxFiles: 1},
		}
		if _, err := SealNew(policy); !errors.Is(err, ErrExecutionPolicyInvalid) {
			t.Fatalf("case-folded glob collision = %v, want invalid", err)
		}
	})
	t.Run("supported glob grammar", func(t *testing.T) {
		policy := testExecutionPolicy()
		policy.ArtifactOutputs = []NodeArtifactOutput{
			{Glob: "reports/report-*.json", MaxBytes: 1, MaxFiles: 1},
			{Glob: "logs/?/entry-[a-z]*", MaxBytes: 1, MaxFiles: 1},
		}
		if _, err := SealNew(policy); err != nil {
			t.Fatalf("supported globs rejected: %v", err)
		}
	})
}

func TestExecutionPolicySeparatesSupervisorAndCompiledBodyRequirements(t *testing.T) {
	sealed, err := SealNew(testExecutionPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if sealed.SupervisorRequirementsHash == sealed.BodyRequirementsHash {
		t.Fatal("supervisor and body requirement sets collapsed to one authorization identity")
	}
	var decoded map[string]any
	if err := json.Unmarshal(sealed.Canonical, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["runner_build_identity"]; ok {
		t.Fatal("observational runner build identity entered the execution policy")
	}
}

func TestExecutionPolicySealOwnsNestedGrantMemory(t *testing.T) {
	policy := testExecutionPolicy()
	sealed, err := SealNew(policy)
	if err != nil {
		t.Fatal(err)
	}
	policy.Actions[1].ToolSlot.Group = "mutated-caller"
	sealed.Policy.Actions[1].ToolSlot.Group = "mutated-return"
	if strings.Contains(string(sealed.Canonical), "mutated") {
		t.Fatal("canonical seal aliases nested caller or returned grant memory")
	}
}

func TestAssistedPlacementRejectsAmbiguousAndCoordinatorTerms(t *testing.T) {
	for _, term := range []string{
		"local", "location=coordinator", "location=unknown", "location=local,gpu",
		"gpu,location=local", "location=local,location=cloud", "location=local,",
	} {
		binding := Binding{NeedsLabels: []string{term}}
		if err := validateAssistedPlacement(binding, []string{"local"}); !errors.Is(err, ErrExecutionPolicyInvalid) {
			t.Errorf("term %q error = %v, want invalid policy", term, err)
		}
	}
	for term, declared := range map[string][]string{"location=local": {"local"}, "gpu": {"cloud", "local"}} {
		if err := validateAssistedPlacement(Binding{NeedsLabels: []string{term}}, declared); err != nil {
			t.Errorf("term %q unexpectedly rejected: %v", term, err)
		}
	}
	if err := validateAssistedPlacement(Binding{NeedsLabels: []string{"location=cloud", "local"}}, []string{"cloud"}); !errors.Is(err, ErrExecutionPolicyInvalid) {
		t.Errorf("conflicting pipeline/node placement error = %v, want invalid policy", err)
	}
	if err := validateAssistedPlacement(Binding{}, []string{"cloud", "local"}); err != nil {
		t.Errorf("unconstrained two-location policy rejected: %v", err)
	}
}

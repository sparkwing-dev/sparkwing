package executionpolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func testExecutionPolicy() nodeExecutionPolicy {
	digest := "sha256:" + strings.Repeat("a", 64)
	return nodeExecutionPolicy{
		Version: nodeExecutionPolicyVersion, Pipeline: "release", NodeID: "deploy", AllowedLocations: []string{"cloud"},
		Dependencies:           []nodeDependencyAuthority{{NodeID: "build", WholeOutput: true, MaxBytes: 1 << 20}},
		SecretNames:            []string{"DEPLOY_TOKEN"},
		ArtifactInputs:         []nodeArtifactInput{{ProducerNodeID: "build", ManifestDigest: digest, Into: "artifacts/build", MaxBytes: 1 << 30, MaxFiles: 1000, MaxManifestBytes: 1 << 20}},
		ArtifactOutputs:        []nodeArtifactOutput{{Glob: "reports/report-*", MaxBytes: 1 << 20, MaxFiles: 100}},
		ArtifactOutputMaxBytes: 1 << 20,
		ResultMaxBytes:         1 << 20,
		Emission: nodeEmissionAuthority{
			Events:  []nodeExecutionEventKind{nodeExecutionEventSummary, nodeExecutionEventAnnotation},
			StepIDs: []string{"publish"}, MaxEvents: 100, MaxPayloadBytes: 1 << 16, MaxTotalBytes: 1 << 20,
		},
		SupervisorRequirements: []string{fleetSupervisorRuntimeRequirement, "process-tree-v1"},
		Body: nodeCompiledBodyAuthority{
			ProtocolVersion: assistedBodyProtocolVersion, RuntimeRequirements: []string{fleetBodyRuntimeRequirement, "actions-v1"},
			Source: nodeBodySourceAuthority{Kind: "git", Identity: strings.Repeat("b", 40), ManifestDigest: digest, PlanDigest: digest},
		},
		Actions: []nodeExecutionGrant{
			{GrantID: "ref-release", Kind: nodeExecutionActionRef, Ref: &nodeRefGrant{Pipeline: "release", NodeID: "notify", MaxBytes: 1 << 10}},
			{GrantID: "tool-docker", Kind: nodeExecutionActionToolSlot, ToolSlot: &nodeToolSlotGrant{Group: "docker", Scope: "box", Capacity: 100, OnLimit: "queue", MaxCost: 1}},
		},
	}
}

func TestExecutionPolicyV1DoesNotPromiseUnimplementedOutputProjection(t *testing.T) {
	sealed, err := sealNew(testExecutionPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed.Canonical), "output_selectors") {
		t.Fatalf("v1 policy exposes an unenforced output projection: %s", sealed.Canonical)
	}
}

func TestExecutionPolicyCanonicalSealIsStable(t *testing.T) {
	policy := testExecutionPolicy()
	first, err := sealNew(policy)
	if err != nil {
		t.Fatal(err)
	}
	policy.SecretNames = []string{"DEPLOY_TOKEN"}
	policy.Emission.Events = []nodeExecutionEventKind{nodeExecutionEventAnnotation, nodeExecutionEventSummary}
	policy.Actions[0], policy.Actions[1] = policy.Actions[1], policy.Actions[0]
	second, err := sealNew(policy)
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
	for name, mutate := range map[string]func(*nodeExecutionPolicy){
		"whitespace":            func(p *nodeExecutionPolicy) { p.NodeID = " deploy" },
		"control":               func(p *nodeExecutionPolicy) { p.SecretNames = []string{"TOKEN\n"} },
		"invalid utf8":          func(p *nodeExecutionPolicy) { p.Pipeline = string([]byte{0xff}) },
		"coordinator placement": func(p *nodeExecutionPolicy) { p.AllowedLocations = []string{"coordinator"} },
		"secret-shaped extension impossible": func(p *nodeExecutionPolicy) {
			p.Actions[0].Await = &nodeAwaitGrant{Pipeline: "steal"}
		},
		"absolute output":     func(p *nodeExecutionPolicy) { p.ArtifactOutputs[0].Glob = "/tmp/**" },
		"traversal output":    func(p *nodeExecutionPolicy) { p.ArtifactOutputs[0].Glob = "../**" },
		"noncanonical digest": func(p *nodeExecutionPolicy) { p.Body.Source.ManifestDigest = strings.Repeat("a", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			policy := testExecutionPolicy()
			mutate(&policy)
			if _, err := sealNew(policy); !errors.Is(err, errExecutionPolicyInvalid) {
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
			if _, err := decode(candidate); !errors.Is(err, errExecutionPolicyInvalid) {
				t.Fatalf("decode error = %v, want ErrExecutionPolicyInvalid", err)
			}
		})
	}
}

func TestExecutionPolicyCarrierRejectsUnknownTrailingAndOwnsBytes(t *testing.T) {
	var owner carrier
	if err := setNew(&owner, testExecutionPolicy()); err != nil {
		t.Fatal(err)
	}
	raw, err := encodeCarrier(&owner)
	if err != nil {
		t.Fatal(err)
	}
	unknown := append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"sentinel_private":"reject"}`)...)
	for name, candidate := range map[string][]byte{
		"unknown":  unknown,
		"trailing": append(append([]byte(nil), raw...), []byte(` {}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			var decoded carrier
			if err := decodeCarrierStrict(candidate, &decoded); !errors.Is(err, errExecutionPolicyInvalid) {
				t.Fatalf("decode %s carrier = %v, want invalid", name, err)
			}
			if isSealed(&decoded) {
				t.Fatalf("decode %s partially changed owner", name)
			}
		})
	}

	raw[0] ^= 0xff
	again, err := encodeCarrier(&owner)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(raw, again) {
		t.Fatal("encoded carrier aliases its owned bytes")
	}
	var copied carrier
	copyCarrier(&copied, &owner)
	clearCarrier(&owner)
	if !isSealed(&copied) {
		t.Fatal("carrier copy aliased cleared source")
	}
}

func TestExecutionPolicyDecodeDispatchesPersistedVersions(t *testing.T) {
	for name, mutate := range map[string]func(*nodeExecutionPolicy){
		"future policy": func(p *nodeExecutionPolicy) { p.Version = nodeExecutionPolicyVersion + 1 },
		"future body":   func(p *nodeExecutionPolicy) { p.Body.ProtocolVersion = assistedBodyProtocolVersion + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			policy := testExecutionPolicy()
			mutate(&policy)
			raw, err := json.Marshal(policy)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decode(raw); !errors.Is(err, errExecutionUpgradeRequired) {
				t.Fatalf("decode error = %v, want upgrade required", err)
			}
			if _, err := sealNew(policy); !errors.Is(err, errExecutionPolicyInvalid) {
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
			if _, err := sealNew(policy); !errors.Is(err, errExecutionPolicyInvalid) {
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
			if _, err := sealNew(policy); !errors.Is(err, errExecutionPolicyInvalid) {
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
		if _, err := sealNew(policy); !errors.Is(err, errExecutionPolicyInvalid) {
			t.Fatalf("case-folded destination collision = %v, want invalid", err)
		}
	})
	t.Run("non-adjacent case-insensitive globs", func(t *testing.T) {
		policy := testExecutionPolicy()
		policy.ArtifactOutputs = []nodeArtifactOutput{
			{Glob: "A/report-*", MaxBytes: 1, MaxFiles: 1},
			{Glob: "B/report-*", MaxBytes: 1, MaxFiles: 1},
			{Glob: "a/report-*", MaxBytes: 1, MaxFiles: 1},
		}
		if _, err := sealNew(policy); !errors.Is(err, errExecutionPolicyInvalid) {
			t.Fatalf("case-folded glob collision = %v, want invalid", err)
		}
	})
	t.Run("supported glob grammar", func(t *testing.T) {
		policy := testExecutionPolicy()
		policy.ArtifactOutputs = []nodeArtifactOutput{
			{Glob: "reports/report-*.json", MaxBytes: 1, MaxFiles: 1},
			{Glob: "logs/?/entry-[a-z]*", MaxBytes: 1, MaxFiles: 1},
		}
		if _, err := sealNew(policy); err != nil {
			t.Fatalf("supported globs rejected: %v", err)
		}
	})
}

func TestExecutionPolicySeparatesSupervisorAndCompiledBodyRequirements(t *testing.T) {
	sealed, err := sealNew(testExecutionPolicy())
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
	sealed, err := sealNew(policy)
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
		binding := binding{NeedsLabels: []string{term}}
		if err := validateAssistedPlacement(binding, []string{"local"}); !errors.Is(err, errExecutionPolicyInvalid) {
			t.Errorf("term %q error = %v, want invalid policy", term, err)
		}
	}
	for term, declared := range map[string][]string{"location=local": {"local"}, "gpu": {"cloud", "local"}} {
		if err := validateAssistedPlacement(binding{NeedsLabels: []string{term}}, declared); err != nil {
			t.Errorf("term %q unexpectedly rejected: %v", term, err)
		}
	}
	if err := validateAssistedPlacement(binding{NeedsLabels: []string{"location=cloud", "local"}}, []string{"cloud"}); !errors.Is(err, errExecutionPolicyInvalid) {
		t.Errorf("conflicting pipeline/node placement error = %v, want invalid policy", err)
	}
	if err := validateAssistedPlacement(binding{}, []string{"cloud", "local"}); err != nil {
		t.Errorf("unconstrained two-location policy rejected: %v", err)
	}
}

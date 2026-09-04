package s3state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/executionpolicy"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestDecodeNodeEnvelopeRejectsExtensionsTrailingDataAndBindingTampering(t *testing.T) {
	node := internalSealedNode(t)
	env, err := encodeNodeEnvelope(*node, "release")
	if err != nil {
		t.Fatal(err)
	}

	unknown := append(append([]byte(nil), env.Data[:len(env.Data)-1]...), []byte(`,"sentinel_private_value":"must-reject"}`)...)
	for name, raw := range map[string][]byte{
		"unknown":  unknown,
		"trailing": append(append([]byte(nil), env.Data...), []byte(` {}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeNodeEnvelope(raw, "release"); err == nil {
				t.Fatalf("%s node envelope accepted", name)
			}
		})
	}

	for name, mutate := range map[string]func(map[string]any){
		"pipeline":  func(_ map[string]any) {},
		"node":      func(record map[string]any) { record["id"] = "other" },
		"deps":      func(record map[string]any) { record["deps"] = []string{"other"} },
		"placement": func(record map[string]any) { record["required_executor_location"] = "local" },
	} {
		t.Run("binding/"+name, func(t *testing.T) {
			var record map[string]any
			if err := json.Unmarshal(env.Data, &record); err != nil {
				t.Fatal(err)
			}
			mutate(record)
			raw, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			pipeline := "release"
			if name == "pipeline" {
				pipeline = "other"
			}
			if _, err := decodeNodeEnvelope(raw, pipeline); !errors.Is(err, executionpolicy.ErrExecutionPolicyInvalid) {
				t.Fatalf("tampered %s binding error = %v, want invalid policy", name, err)
			}
		})
	}
}

func TestNodeEnvelopeCarrierIsPrivateAndRoundTripsExactly(t *testing.T) {
	node := internalSealedNode(t)
	want, err := executionpolicy.EncodeCarrier(node)
	if err != nil {
		t.Fatal(err)
	}
	env, err := encodeNodeEnvelope(*node, "release")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(env.Data, []byte(`"execution_policy"`)) {
		t.Fatalf("private carrier missing from S3 envelope: %s", env.Data)
	}
	public := mustPublicNodeJSON(t, &node.Node)
	if bytes.Contains(public, []byte(`"execution_policy"`)) || bytes.Contains(public, []byte(`"policy_hash"`)) {
		t.Fatalf("public Node JSON exposed private carrier: %s", public)
	}
	got, err := decodeNodeEnvelope(env.Data, "release")
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, err := executionpolicy.EncodeCarrier(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(roundTrip, want) {
		t.Fatalf("carrier changed during S3 round trip:\n%s\n%s", want, roundTrip)
	}
}

func TestS3PrivateNodeRecordRejectsCorruptionAndSealedDepsRewrite(t *testing.T) {
	ctx := context.Background()
	node := internalSealedNode(t)
	wantCarrier, err := executionpolicy.EncodeCarrier(node)
	if err != nil {
		t.Fatal(err)
	}
	env, err := encodeNodeEnvelope(*node, "release")
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := executionpolicy.PersistenceOf(node)
	if err != nil {
		t.Fatal(err)
	}
	badHash := "sha256:" + strings.Repeat("f", 64)
	corrupt := bytes.ReplaceAll(env.Data, []byte(persisted.PolicyHash), []byte(badHash))
	if _, err := decodeNodeEnvelope(corrupt, "release"); !errors.Is(err, executionpolicy.ErrExecutionPolicyInvalid) {
		t.Fatalf("corrupt private seal = %v, want invalid policy", err)
	}

	art, err := fs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := New(art)
	t.Cleanup(func() { _ = b.Close() })
	if err := b.CreateRun(ctx, store.Run{ID: "sealed-run", Pipeline: "release", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := b.appendEnvelope(ctx, "sealed-run", env); err != nil {
		t.Fatal(err)
	}
	got, err := b.GetNode(ctx, "sealed-run", "deploy")
	if err != nil {
		t.Fatal(err)
	}
	got.Deps = append(got.Deps, "caller-mutation")
	listed, err := b.ListNodes(ctx, "sealed-run")
	if err != nil {
		t.Fatal(err)
	}
	listed[0].NeedsLabels = append(listed[0].NeedsLabels, "caller-mutation")

	rs := b.lookupRun("sealed-run")
	rs.mu.Lock()
	stored := cloneNodeRecord(rs.nodes["deploy"])
	rs.mu.Unlock()
	storedCarrier, err := executionpolicy.EncodeCarrier(stored)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedCarrier, wantCarrier) || len(stored.Deps) != 0 || len(stored.NeedsLabels) != 0 {
		t.Fatalf("public Get/List copy mutated private record: %+v", stored.Node)
	}
	if err := b.UpdateNodeDeps(ctx, "sealed-run", "deploy", []string{"widened"}); !errors.Is(err, executionpolicy.ErrExecutionPolicyInvalid) {
		t.Fatalf("sealed deps rewrite = %v, want invalid policy", err)
	}
}

func internalSealedNode(t *testing.T) *nodeRecord {
	t.Helper()
	node := &nodeRecord{Node: store.Node{RunID: "sealed-run", NodeID: "deploy", Status: "pending"}}
	digest := "sha256:" + strings.Repeat("a", 64)
	policy := executionpolicy.NodeExecutionPolicy{
		Version: executionpolicy.NodeExecutionPolicyVersion, Pipeline: "release", NodeID: "deploy",
		AllowedLocations: []string{"cloud", "local"}, ResultMaxBytes: 1,
		SupervisorRequirements: []string{"fleet-supervisor-v1"},
		Body: executionpolicy.NodeCompiledBodyAuthority{
			ProtocolVersion: executionpolicy.AssistedBodyProtocolVersion, RuntimeRequirements: []string{"fleet-body-v1"},
			Source: executionpolicy.NodeBodySourceAuthority{
				Kind: "git", Identity: strings.Repeat("a", 40), ManifestDigest: digest, PlanDigest: digest,
			},
		},
	}
	if err := executionpolicy.SetNew(node, policy); err != nil {
		t.Fatal(err)
	}
	return node
}

func mustPublicNodeJSON(t *testing.T, node *store.Node) []byte {
	t.Helper()
	raw, err := json.Marshal(node)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

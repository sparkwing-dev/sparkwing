package livechainacceptance

import (
	"context"
	"strings"
	"testing"
	"time"

	storagefs "github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
)

func TestNewProductionDependenciesWiresDurableAuthorities(t *testing.T) {
	writer, err := storagefs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, _, script := validTwoFlowScript(t)
	clock := fixedClock{now: time.Now().UTC()}
	kmsClient := &deterministicKMS{keyID: testKMSKeyARN}
	effectAdapter := &idempotentEffectAdapter{results: make(map[string]EffectResult), creates: make(map[string]int), loseResponse: make(map[string]bool)}
	config := ProductionDependenciesConfig{
		Prefix: "live-chain", Objects: distributedTestStore{writer},
		KMSSigner: kmsClient, KMSVerifier: verifyOnlyKMS{client: kmsClient}, KMSKeyARN: testKMSKeyARN,
		EffectAdapter: effectAdapter, Authority: testAuthority{}, Production: script, Artifacts: script,
		Health: script, Faults: script, Clock: clock,
	}
	deps, err := NewProductionDependencies(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := deps.Sessions.(*CASSessionStore); !ok {
		t.Fatalf("session store = %T, want *CASSessionStore", deps.Sessions)
	}
	if _, ok := deps.Effects.(*DurableEffectExecutor); !ok {
		t.Fatalf("effect executor = %T, want *DurableEffectExecutor", deps.Effects)
	}
	if deps.Production != script || deps.Artifacts != script || deps.Health != script || deps.Faults != script || deps.Clock != clock {
		t.Fatal("runtime authorities were not preserved exactly")
	}
	if deps.StateSigner == nil || deps.StateVerifier == nil {
		t.Fatal("KMS session authority is incomplete")
	}
	if _, exposesSigning := deps.StateVerifier.(SessionSigner); exposesSigning {
		t.Fatal("production session verifier exposes signing authority")
	}
}

func TestNewProductionDependenciesRejectsEveryMissingAuthority(t *testing.T) {
	writer, err := storagefs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, _, script := validTwoFlowScript(t)
	clock := fixedClock{now: time.Now().UTC()}
	kmsClient := &deterministicKMS{keyID: testKMSKeyARN}
	base := ProductionDependenciesConfig{
		Prefix: "live-chain", Objects: distributedTestStore{writer},
		KMSSigner: kmsClient, KMSVerifier: verifyOnlyKMS{client: kmsClient}, KMSKeyARN: testKMSKeyARN,
		EffectAdapter: &idempotentEffectAdapter{results: make(map[string]EffectResult), creates: make(map[string]int), loseResponse: make(map[string]bool)},
		Authority:     testAuthority{}, Production: script, Artifacts: script, Health: script, Faults: script, Clock: clock,
	}
	tests := map[string]func(*ProductionDependenciesConfig){
		"prefix":         func(c *ProductionDependenciesConfig) { c.Prefix = "" },
		"objects":        func(c *ProductionDependenciesConfig) { c.Objects = nil },
		"KMS signer":     func(c *ProductionDependenciesConfig) { c.KMSSigner = nil },
		"KMS verifier":   func(c *ProductionDependenciesConfig) { c.KMSVerifier = nil },
		"KMS key":        func(c *ProductionDependenciesConfig) { c.KMSKeyARN = "" },
		"effect adapter": func(c *ProductionDependenciesConfig) { c.EffectAdapter = nil },
		"authority":      func(c *ProductionDependenciesConfig) { c.Authority = nil },
		"production":     func(c *ProductionDependenciesConfig) { c.Production = nil },
		"artifacts":      func(c *ProductionDependenciesConfig) { c.Artifacts = nil },
		"health":         func(c *ProductionDependenciesConfig) { c.Health = nil },
		"faults":         func(c *ProductionDependenciesConfig) { c.Faults = nil },
		"clock":          func(c *ProductionDependenciesConfig) { c.Clock = nil },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			config := base
			mutate(&config)
			if _, err := NewProductionDependencies(config); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("missing %s error = %v", name, err)
			}
		})
	}
}

func TestProductionDependenciesRunOnlyThroughDurableSession(t *testing.T) {
	writer, err := storagefs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, b, script := validTwoFlowScript(t)
	clock := fixedClock{now: b.LandedAt.Add(time.Minute)}
	kmsClient := &deterministicKMS{keyID: testKMSKeyARN}
	deps, err := NewProductionDependencies(ProductionDependenciesConfig{
		Prefix: "live-chain", Objects: distributedTestStore{writer},
		KMSSigner: kmsClient, KMSVerifier: verifyOnlyKMS{client: kmsClient}, KMSKeyARN: testKMSKeyARN,
		EffectAdapter: &scriptedIdempotentAdapter{script: script, results: make(map[string]EffectResult)},
		Authority:     testAuthority{}, Production: script, Artifacts: script, Health: script, Faults: script, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	seed := SessionSeed{ID: "production-construction", Events: [2]LandEvent{a, b}}
	proof, err := RunSession(context.Background(), seed, deps)
	if err != nil {
		t.Fatal(err)
	}
	if proof.Events != seed.Events || proof.Rollback.Commit != a.Commit {
		t.Fatalf("production proof = %+v", proof)
	}
}

type scriptedIdempotentAdapter struct {
	script  *scriptedAcceptance
	results map[string]EffectResult
}

func (adapter *scriptedIdempotentAdapter) Apply(ctx context.Context, request EffectRequest) (EffectResult, error) {
	if result, ok := adapter.results[request.ID]; ok {
		return result, nil
	}
	var result EffectResult
	var err error
	switch request.Kind {
	case EffectDeployA, EffectDeployB:
		result.Deployment, err = adapter.script.Deploy(ctx, request.Artifact)
	case EffectRollback:
		result.Deployment, err = adapter.script.Rollback(ctx, request.Deployment)
	case EffectNotifyAcceptedA, EffectNotifyAcceptedB, EffectNotifyFailure, EffectNotifyRollback:
		result.Notification, err = adapter.script.Notify(ctx, request.Notification)
	case EffectInjectFailure:
		result.Fault, err = adapter.script.InjectFailure(ctx, request.Deployment)
		result.Fault.ID = request.ID
	case EffectRemoveFailure:
		result.Cleanup, err = adapter.script.RemoveFailure(ctx, request.Cleanup)
	default:
		err = ErrSessionConflict
	}
	if err == nil {
		adapter.results[request.ID] = result
	}
	return result, err
}

func (adapter *scriptedIdempotentAdapter) Reconcile(_ context.Context, request EffectRequest) (EffectResult, bool, error) {
	result, ok := adapter.results[request.ID]
	return result, ok, nil
}

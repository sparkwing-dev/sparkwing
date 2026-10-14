package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestFilePlanCacheRefusesForeignScope(t *testing.T) {
	inputs := planCacheInputs()
	key, _, err := PlanCacheIdentity(inputs)
	if err != nil {
		t.Fatal(err)
	}
	cache, err := NewFilePlanCache(t.TempDir(), inputs.Team, inputs.Repository)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, foreign := range []PlanCacheKey{
		{Version: key.Version, Team: "other", Repository: key.Repository, Digest: key.Digest},
		{Version: key.Version, Team: key.Team, Repository: "gh:456", Digest: key.Digest},
		{Version: key.Version, Team: key.Team, Repository: key.Repository, Digest: "../../escape"},
	} {
		if hit, err := cache.Lookup(ctx, foreign); err == nil || hit.Entry != nil {
			t.Fatalf("foreign lookup = %+v, %v", hit, err)
		}
		if err := cache.Put(ctx, PlanCacheEntry{Key: foreign}); err == nil {
			t.Fatal("foreign write succeeded")
		}
		if err := cache.Invalidate(ctx, foreign, "foreign"); err == nil {
			t.Fatal("foreign invalidation succeeded")
		}
	}
}

func TestFilePlanCacheDamagedBytesMissAndAreRemoved(t *testing.T) {
	inputs := planCacheInputs()
	key, _, err := PlanCacheIdentity(inputs)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	cache, err := NewFilePlanCache(directory, inputs.Team, inputs.Repository)
	if err != nil {
		t.Fatal(err)
	}
	entry := PlanCacheEntry{Key: key, Inputs: inputs, Plan: json.RawMessage(`{"jobs":[]}`), PlanHash: runspec.Hash(strings.Repeat("c", 64))}
	ctx := context.Background()
	if err := cache.Put(ctx, entry); err != nil {
		t.Fatal(err)
	}
	var stored string
	err = filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			if err := fssecure.VerifyPrivateConfig(path, info); err != nil {
				t.Errorf("cache file is not private: %v", err)
			}
			stored = path
		}
		return nil
	})
	if err != nil || stored == "" {
		t.Fatalf("locate cache entry: %q, %v", stored, err)
	}
	if err := os.WriteFile(stored, []byte("damaged cache bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	miss, err := cache.Lookup(ctx, key)
	if err != nil || miss.Entry != nil || miss.MissReason == "" {
		t.Fatalf("damaged lookup = %+v, %v", miss, err)
	}
	if _, err := os.Stat(stored); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("damaged file survived lookup: %v", err)
	}
	if err := os.Mkdir(stored, 0o700); err != nil {
		t.Fatal(err)
	}
	miss, err = cache.Lookup(ctx, key)
	if err == nil || miss.Entry != nil || miss.MissReason == "" {
		t.Fatalf("unreadable lookup = %+v, %v", miss, err)
	}
	if strings.Contains(err.Error(), string(key.Digest)) {
		t.Fatalf("cache error exposed a private input digest: %v", err)
	}
}

func TestFilePlanCacheCancellationNeverPublishes(t *testing.T) {
	inputs := planCacheInputs()
	key, _, err := PlanCacheIdentity(inputs)
	if err != nil {
		t.Fatal(err)
	}
	cache, err := NewFilePlanCache(t.TempDir(), inputs.Team, inputs.Repository)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := cache.Put(ctx, PlanCacheEntry{Key: key}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled put = %v", err)
	}
	if hit, err := cache.Lookup(ctx, key); !errors.Is(err, context.Canceled) || hit.Entry != nil {
		t.Fatalf("cancelled lookup = %+v, %v", hit, err)
	}
	if err := cache.Invalidate(ctx, key, "cancelled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled invalidation = %v", err)
	}
}

func TestValidatePlanCacheHitRefusesStaleIdentityAndExpansion(t *testing.T) {
	inputs := planCacheInputs()
	key, _, err := PlanCacheIdentity(inputs)
	if err != nil {
		t.Fatal(err)
	}
	entry := PlanCacheEntry{Key: key, Inputs: inputs, Plan: json.RawMessage(`{"jobs":[]}`), PlanHash: runspec.Hash(strings.Repeat("c", 64))}
	validation := nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: planCacheSecretMatcher{}}
	cases := map[string]func(*PlanCacheEntry, *PlanCacheInputs, *nodeproto.ValidationContext){
		"changed current inputs": func(_ *PlanCacheEntry, i *PlanCacheInputs, _ *nodeproto.ValidationContext) {
			i.SecretVersion = "new-registry"
		},
		"corrupted stored inputs": func(e *PlanCacheEntry, _ *PlanCacheInputs, _ *nodeproto.ValidationContext) {
			e.Inputs.Pipeline = "other"
		},
		"foreign key":       func(e *PlanCacheEntry, _ *PlanCacheInputs, _ *nodeproto.ValidationContext) { e.Key.Team = "other" },
		"incomplete inputs": func(_ *PlanCacheEntry, i *PlanCacheInputs, _ *nodeproto.ValidationContext) { i.RunContextDigest = "" },
		"expansion":         func(_ *PlanCacheEntry, _ *PlanCacheInputs, v *nodeproto.ValidationContext) { v.Expansion = true },
		"expansion jobs": func(_ *PlanCacheEntry, _ *PlanCacheInputs, v *nodeproto.ValidationContext) {
			v.ExistingJobs = []nodeproto.JobSpec{{ID: "prior"}}
		},
		"expansion source":        func(_ *PlanCacheEntry, _ *PlanCacheInputs, v *nodeproto.ValidationContext) { v.SourceJob = "prior" },
		"missing secret registry": func(_ *PlanCacheEntry, _ *PlanCacheInputs, v *nodeproto.ValidationContext) { v.Secrets = nil },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			entry, inputs, validation := entry, inputs, validation
			change(&entry, &inputs, &validation)
			hit, err := ValidatePlanCacheHit(entry, inputs, validation)
			if err == nil || hit.Key != (PlanCacheKey{}) || hit.PlanHash != "" {
				t.Fatalf("invalid cached hit = %+v, %v", hit, err)
			}
		})
	}
}

type planCacheSecretMatcher struct{}

func (planCacheSecretMatcher) ContainsSecret(string) bool { return false }

func TestFilePlanCachePersistsScopedDeclarations(t *testing.T) {
	inputs := planCacheInputs()
	key, _, err := PlanCacheIdentity(inputs)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	cache, err := NewFilePlanCache(directory, inputs.Team, inputs.Repository)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	miss, err := cache.Lookup(ctx, key)
	if err != nil || miss.Entry != nil || miss.MissReason == "" {
		t.Fatalf("empty cache = %+v, %v", miss, err)
	}
	entry := PlanCacheEntry{Key: key, Inputs: inputs, Plan: json.RawMessage(`{"jobs":[]}`), PlanHash: runspec.Hash(strings.Repeat("c", 64))}
	if err := cache.Put(ctx, entry); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFilePlanCache(directory, inputs.Team, inputs.Repository)
	if err != nil {
		t.Fatal(err)
	}
	hit, err := reopened.Lookup(ctx, key)
	if err != nil || hit.Entry == nil || hit.MissReason != "" {
		t.Fatalf("persisted lookup = %+v, %v", hit, err)
	}
	if hit.Entry.Key != key || hit.Entry.Inputs != inputs || string(hit.Entry.Plan) != string(entry.Plan) || hit.Entry.PlanHash != entry.PlanHash {
		t.Fatalf("stored declaration changed: %+v", hit.Entry)
	}
	entry.Plan = json.RawMessage(`{"jobs":[{"id":"replacement"}]}`)
	if err := reopened.Put(ctx, entry); err != nil {
		t.Fatal(err)
	}
	hit, err = cache.Lookup(ctx, key)
	if err != nil || hit.Entry == nil || string(hit.Entry.Plan) != string(entry.Plan) {
		t.Fatalf("replacement lookup = %+v, %v", hit, err)
	}
	if err := cache.Invalidate(ctx, key, "secret registry changed"); err != nil {
		t.Fatal(err)
	}
	miss, err = reopened.Lookup(ctx, key)
	if err != nil || miss.Entry != nil || miss.MissReason == "" {
		t.Fatalf("invalidated cache = %+v, %v", miss, err)
	}
	if err := cache.Invalidate(ctx, key, "already missing"); err != nil {
		t.Fatal(err)
	}
}

func TestPlanCacheIdentityKeepsCompleteInputs(t *testing.T) {
	inputs := planCacheInputs()
	key, eligible, err := PlanCacheIdentity(inputs)
	if err != nil || !eligible.Eligible || key.Team != inputs.Team || key.Repository != inputs.Repository || key.Version != inputs.Version || len(key.Digest) != 64 {
		t.Fatalf("identity = %+v, %+v, %v", key, eligible, err)
	}
	again, eligible, err := PlanCacheIdentity(inputs)
	if err != nil || !eligible.Eligible || again != key {
		t.Fatalf("same inputs changed identity: %+v, %+v, %v", again, eligible, err)
	}
	inputs.RunContextDigest = runspec.Hash(strings.Repeat("b", 64))
	changed, eligible, err := PlanCacheIdentity(inputs)
	if err != nil || !eligible.Eligible || changed == key {
		t.Fatalf("changed SDK-visible run context reused identity: %+v, %+v, %v", changed, eligible, err)
	}
}

func TestPlanCacheIdentitySeparatesPlanningInputs(t *testing.T) {
	base, _, err := PlanCacheIdentity(planCacheInputs())
	if err != nil {
		t.Fatal(err)
	}
	other := runspec.Hash(strings.Repeat("b", 64))
	changes := map[string]func(*PlanCacheInputs){
		"version":          func(i *PlanCacheInputs) { i.Version++ },
		"team":             func(i *PlanCacheInputs) { i.Team = "team-b" },
		"repository":       func(i *PlanCacheInputs) { i.Repository = "gh:456" },
		"sdk":              func(i *PlanCacheInputs) { i.Engine.Pin.SDK = engine.TypeScriptSDK },
		"sdk version":      func(i *PlanCacheInputs) { i.Engine.Pin.Version = "v2.0.0" },
		"sdk manifest":     func(i *PlanCacheInputs) { i.Engine.Pin.Manifest = "nested/go.mod" },
		"sdk lockfile":     func(i *PlanCacheInputs) { i.Engine.Pin.Lockfile = "go.sum" },
		"sdk input digest": func(i *PlanCacheInputs) { i.Engine.Pin.InputsDigest = string(other) },
		"engine version":   func(i *PlanCacheInputs) { i.Engine.Version = "v2.0.0" },
		"engine manifest":  func(i *PlanCacheInputs) { i.Engine.ManifestDigest = string(other) },
		"pipeline":         func(i *PlanCacheInputs) { i.Pipeline = "release" },
		"entrypoint":       func(i *PlanCacheInputs) { i.Entrypoint = "nested/main.go" },
		"source":           func(i *PlanCacheInputs) { i.SourceDigest = other },
		"entry":            func(i *PlanCacheInputs) { i.EntryDigest = other },
		"project":          func(i *PlanCacheInputs) { i.ProjectDigest = other },
		"args":             func(i *PlanCacheInputs) { i.ArgsDigest = other },
		"environment":      func(i *PlanCacheInputs) { i.EnvironmentDigest = other },
		"run context":      func(i *PlanCacheInputs) { i.RunContextDigest = other },
		"runner":           func(i *PlanCacheInputs) { i.RunnerDigest = other },
		"checkout":         func(i *PlanCacheInputs) { i.CheckoutDigest = other },
		"describe":         func(i *PlanCacheInputs) { i.DescribeDigest = other },
		"secret registry":  func(i *PlanCacheInputs) { i.SecretVersion = "registry-2" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			inputs := planCacheInputs()
			change(&inputs)
			key, eligible, err := PlanCacheIdentity(inputs)
			if err != nil || !eligible.Eligible || key == base {
				t.Fatalf("changed %s reused identity: %+v, %+v, %v", name, key, eligible, err)
			}
		})
	}
}

func TestPlanCacheIncompleteInputsBypassWithoutRejectingRun(t *testing.T) {
	cases := map[string]func(*PlanCacheInputs){
		"version":         func(i *PlanCacheInputs) { i.Version = 0 },
		"team":            func(i *PlanCacheInputs) { i.Team = "" },
		"repository":      func(i *PlanCacheInputs) { i.Repository = "" },
		"engine":          func(i *PlanCacheInputs) { i.Engine = engine.Identity{} },
		"pipeline":        func(i *PlanCacheInputs) { i.Pipeline = "" },
		"entrypoint":      func(i *PlanCacheInputs) { i.Entrypoint = "" },
		"source":          func(i *PlanCacheInputs) { i.SourceDigest = "" },
		"entry":           func(i *PlanCacheInputs) { i.EntryDigest = "" },
		"project":         func(i *PlanCacheInputs) { i.ProjectDigest = "" },
		"args":            func(i *PlanCacheInputs) { i.ArgsDigest = "" },
		"environment":     func(i *PlanCacheInputs) { i.EnvironmentDigest = "" },
		"run context":     func(i *PlanCacheInputs) { i.RunContextDigest = "" },
		"runner":          func(i *PlanCacheInputs) { i.RunnerDigest = "" },
		"checkout":        func(i *PlanCacheInputs) { i.CheckoutDigest = "" },
		"describe":        func(i *PlanCacheInputs) { i.DescribeDigest = "" },
		"secret registry": func(i *PlanCacheInputs) { i.SecretVersion = "" },
		"invalid digest":  func(i *PlanCacheInputs) { i.ArgsDigest = "missing" },
		"invalid text":    func(i *PlanCacheInputs) { i.Pipeline = "\xff" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			inputs := planCacheInputs()
			change(&inputs)
			key, eligible, err := PlanCacheIdentity(inputs)
			if err != nil || eligible.Eligible || eligible.Reason == "" || key != (PlanCacheKey{}) {
				t.Fatalf("incomplete %s = %+v, %+v, %v; want explained bypass", name, key, eligible, err)
			}
		})
	}
}

func planCacheInputs() PlanCacheInputs {
	digest := strings.Repeat("a", 64)
	return PlanCacheInputs{
		Version: 1, Team: "team-a", Repository: "gh:123", Pipeline: "checks", Entrypoint: ".sparkwing/main.go",
		Engine: engine.Identity{
			Pin:     engine.Pin{SDK: engine.GoSDK, Version: "v1.2.3", Manifest: "go.mod", InputsDigest: digest},
			Version: "v1.2.3", ManifestDigest: digest,
		},
		SourceDigest: runspec.Hash(digest), EntryDigest: runspec.Hash(digest), ProjectDigest: runspec.Hash(digest),
		ArgsDigest: runspec.Hash(digest), EnvironmentDigest: runspec.Hash(digest), RunContextDigest: runspec.Hash(digest),
		RunnerDigest: runspec.Hash(digest), CheckoutDigest: runspec.Hash(digest), DescribeDigest: runspec.Hash(digest),
		SecretVersion: "registry-1",
	}
}

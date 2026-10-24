package launcher

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
)

type initAccess struct {
	err error
}

func (a initAccess) GitCredential(context.Context, source.Spec) (source.Credential, error) {
	return source.Credential{Kind: "https", Host: "1.1.1.1", Username: "x-access-token", Secret: "source-secret"}, a.err
}

func (initAccess) BundleURL(context.Context, string) (string, error) {
	return "", errors.New("unexpected source bundle")
}

func initSourceFixture(t *testing.T, kind capability.UnitKind) (CloudJobAward, source.Options, string) {
	t.Helper()
	repository, sha := sourcetest.Repository(t)
	remote := "https://1.1.1.1/team/repo"
	sourcetest.Remote(t, map[string]string{remote: repository})
	award, _ := bootstrapAward(t, kind)
	award.Source = source.Spec{RepoKey: "git:1.1.1.1/team/repo", RemoteURL: remote, SHA: sha, PipelineDir: ".sparkwing", Engine: award.Engine}
	options := source.Options{Access: initAccess{}, Base: procenv.Base{Ambient: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment}
	return award, options, filepath.Join(t.TempDir(), "source")
}

func TestSourceInitPublishesExactCheckoutForEveryCloudUnit(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval, capability.UnitNode} {
		t.Run(string(kind), func(t *testing.T) {
			award, options, destination := initSourceFixture(t, kind)
			closed := false
			initializer := SourceInit{Bind: func(ctx context.Context, got CloudJobAward) (source.Options, func() error, error) {
				if !reflect.DeepEqual(got, award) || ctx.Err() != nil {
					t.Fatal("source binding changed its private award")
				}
				return options, func() error { closed = true; return nil }, nil
			}}
			if err := initializer.Run(t.Context(), award, destination); err != nil || !closed {
				t.Fatalf("source init: closed=%t error=%v", closed, err)
			}
			if sha := sourcetest.Git(t, destination, "rev-parse", "HEAD"); sha != award.Source.SHA {
				t.Fatal("published a different commit")
			}
			if _, err := os.Stat(filepath.Join(destination, ".sparkwing", "pipeline.go")); err != nil {
				t.Fatal(err)
			}
			assertInitFiles(t, destination, true)
		})
	}
}

func assertInitFiles(t testing.TB, destination string, published bool) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(destination))
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	if published {
		want = 1
	}
	if len(entries) != want || published && entries[0].Name() != filepath.Base(destination) {
		t.Fatalf("source staging leaked files: %v", entries)
	}
}

func TestSourceInitRefusalAndCancellationLeaveNoCheckout(t *testing.T) {
	for _, mode := range []string{"access", "cancel", "partial_bind", "missing_commit"} {
		t.Run(mode, func(t *testing.T) {
			award, options, destination := initSourceFixture(t, capability.UnitPlan)
			failure := errors.New("source denied")
			cleanup := errors.New("source cleanup")
			closed := false
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			initializer := SourceInit{Bind: func(context.Context, CloudJobAward) (source.Options, func() error, error) {
				var err error
				switch mode {
				case "access":
					options.Access = initAccess{err: failure}
				case "cancel":
					cancel(failure)
				case "partial_bind":
					err = failure
				}
				return options, func() error { closed = true; return cleanup }, err
			}}
			if mode == "missing_commit" {
				award.Source.SHA = strings.Repeat("f", 40)
			}
			err := initializer.Run(ctx, award, destination)
			if err == nil || !closed || !errors.Is(err, cleanup) || strings.Contains(err.Error(), "source-secret") {
				t.Fatalf("failed source init: closed=%t error=%v", closed, err)
			}
			if mode != "missing_commit" && !errors.Is(err, failure) {
				t.Fatal(err)
			}
			assertInitFiles(t, destination, false)
		})
	}
}

func FuzzSourceInitRejectsChangedEngine(f *testing.F) {
	f.Add("v1.2.3", strings.Repeat("a", 64), false)
	f.Add("v9.0.0", "changed", true)
	f.Fuzz(func(t *testing.T, version, manifest string, local bool) {
		award, _ := bootstrapAward(t, capability.UnitNode)
		identity := *award.Engine
		identity.Version, identity.ManifestDigest = version, manifest
		award.Source = source.Spec{RepoKey: "git:github.com/team/repo", RemoteURL: "https://github.com/team/repo", SHA: strings.Repeat("a", 40), Engine: &identity}
		if local {
			award.Source.LocalCheckout = t.TempDir()
		}
		called := false
		failure := errors.New("source binding")
		initializer := SourceInit{Bind: func(context.Context, CloudJobAward) (source.Options, func() error, error) {
			called = true
			return source.Options{}, nil, failure
		}}
		err := initializer.Run(t.Context(), award, filepath.Join(t.TempDir(), "checkout"))
		if identity != *award.Engine || local {
			if called || err == nil {
				t.Fatal("changed engine or local path reached source authority")
			}
		} else if !called || !errors.Is(err, failure) {
			t.Fatal("matching source never reached its binding")
		}
	})
}

func TestSourceInitCapturesSourceBeforeBinding(t *testing.T) {
	award, options, destination := initSourceFixture(t, capability.UnitNode)
	depth := 1
	award.Source.Checkout.Policy.Depth = &depth
	initializer := SourceInit{Bind: func(_ context.Context, input CloudJobAward) (source.Options, func() error, error) {
		*input.Source.Engine = engineid.Identity{}
		*input.Source.Checkout.Policy.Depth = -1
		return options, nil, nil
	}}
	if err := initializer.Run(t.Context(), award, destination); err != nil {
		t.Fatal(err)
	}
	assertInitFiles(t, destination, true)
}

func TestSourceInitPreservesExistingDestination(t *testing.T) {
	award, _ := bootstrapAward(t, capability.UnitPlan)
	award.Source = source.Spec{RepoKey: "git:github.com/team/repo", RemoteURL: "https://github.com/team/repo", SHA: strings.Repeat("a", 40)}
	destination := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(destination, []byte("previous checkout"), 0o600); err != nil {
		t.Fatal(err)
	}
	initializer := SourceInit{Bind: func(context.Context, CloudJobAward) (source.Options, func() error, error) {
		t.Fatal("existing destination reached source authority")
		return source.Options{}, nil, nil
	}}
	if err := initializer.Run(t.Context(), award, destination); err == nil {
		t.Fatal("replaced an existing destination")
	}
	body, err := os.ReadFile(destination)
	if err != nil || string(body) != "previous checkout" {
		t.Fatalf("existing checkout changed: %q %v", body, err)
	}
}

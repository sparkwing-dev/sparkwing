package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestRunnerPolicyBuildKinds(t *testing.T) {
	for _, version := range []string{"(devel)", "(unknown)", "v1.2.3", ""} {
		t.Run(version, func(t *testing.T) {
			var output bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			requests := 0
			resolver, err := engine.NewResolver(engine.Config{BuildVersion: version, CacheDirectory: t.TempDir(), HTTPClient: &http.Client{Transport: intakeGitHubTransport(func(r *http.Request) (*http.Response, error) {
				requests++
				if r.URL.String() != engine.VersionIndexURL {
					t.Errorf("unexpected policy URL %s", r.URL)
				}
				return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			policy := &SignedRunnerAdmission{Resolver: resolver}
			development := version == "(devel)" || version == "(unknown)"
			for range 2 {
				err := policy.CheckRunner(t.Context(), "v1.2.3")
				if development && err != nil || !development && (err == nil || !strings.Contains(err.Error(), "HTTP 403")) {
					t.Fatalf("build %q policy result: %v", version, err)
				}
				_, err = (&SignedEngineVersionPolicy{Resolver: resolver}).notices(t.Context(), "v1.2.3")
				if development && err != nil || !development && (err == nil || !strings.Contains(err.Error(), "HTTP 403")) {
					t.Fatalf("build %q run-page policy result: %v", version, err)
				}
				compatibility, err := (&SignedEngineVersionPolicy{Resolver: resolver}).compatibility(t.Context(), "v1.2.3")
				if development && (err != nil || compatibility.EngineRelease != "v1.2.3") || !development && (err == nil || !strings.Contains(err.Error(), "HTTP 403")) {
					t.Fatalf("build %q compatibility policy result: %+v: %v", version, compatibility, err)
				}
			}
			if development {
				if requests != 0 || strings.Count(output.String(), "policy checks are off for a development build") != 1 {
					t.Fatalf("development policy: requests=%d log=%q", requests, output.String())
				}
			} else if requests != 6 || output.Len() != 0 {
				t.Fatalf("release policy: requests=%d log=%q", requests, output.String())
			}
		})
	}
}

func TestReleasedBuildRunnerInputCannotDisablePolicy(t *testing.T) {
	resolver, err := engine.NewResolver(engine.Config{BuildVersion: "v1.2.3", CacheDirectory: t.TempDir(), HTTPClient: &http.Client{Transport: intakeGitHubTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	policy := &SignedRunnerAdmission{Resolver: resolver}
	if err := quick.Check(func(runnerVersion string) bool {
		err := policy.CheckRunner(t.Context(), runnerVersion)
		return err != nil && strings.Contains(err.Error(), "HTTP 403")
	}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestOnlyTheDeploymentLauncherSkipsRunnerVersionPolicy(t *testing.T) {
	server, _ := engineRuntimeStore(t)
	var checked []string
	server.RunnerPolicy = runnerAdmissionFunc(func(_ context.Context, version string) error {
		checked = append(checked, version)
		return &apierr.Error{Code: apierr.VersionUnsupported, Message: "Release is not listed in the verified version index"}
	})
	insert := func(prefix, team, kind string, pools []string) {
		root := capability.Capability{Prefix: prefix, Team: team, Subject: capability.Subject{Kind: capability.SubjectKind(kind), ID: prefix}, Audience: capability.Controller, Grants: []capability.Grant{capability.ClaimsLaunch}, Binding: capability.Binding{Kind: capability.BindingNone}, Issuance: capability.Delegate, ReservedPools: pools, Repositories: []string{"gh:1"}, IssuedAt: time.Unix(0, 1).UTC()}
		envelope, err := json.Marshal(store.CapabilityRecord{Version: 1, Value: root})
		if err != nil {
			t.Fatal(err)
		}
		poolJSON, err := json.Marshal(pools)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte(prefix + "-secret"))
		var teamColumn any
		if team != "" {
			teamColumn = team
		}
		engineSQL(t, server.Store, `INSERT INTO capabilities(digest,prefix,team,subject_kind,subject_id,audience,grants,issuance,authority_version,authority_json,bind_kind,reserved_pools,repo_scope,created_at,created_by) VALUES (?,?,?,?,?,'controller',?,'delegate',1,?,'none',?,?,1,?)`,
			hex.EncodeToString(digest[:]), prefix, teamColumn, kind, prefix, []byte(`["claims.launch"]`), envelope, poolJSON, []byte(`["gh:1"]`), prefix)
	}
	insert("launcher", "", "service", []string{})
	insert("pool-launcher", "a", "machine", []string{"pool"})
	principal, err := server.CheckRunner(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "pool-launcher"}, "v0.68.6-3-gabc1234")
	var failure *apierr.Error
	if !capability.Launcher(principal.Capability) || capability.CloudLauncher(principal.Capability) || !errors.As(err, &failure) || failure.Code != apierr.VersionUnsupported {
		t.Fatalf("team-pool launcher at an unlisted version: launcher=%v cloud=%v: %v", capability.Launcher(principal.Capability), capability.CloudLauncher(principal.Capability), err)
	}
	principal, err = server.CheckRunner(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "launcher"}, "v0.68.6-3-gabc1234")
	if err != nil || !capability.CloudLauncher(principal.Capability) {
		t.Fatalf("deployment launcher at an unlisted version: %v", err)
	}
	if len(checked) != 1 || checked[0] != "v0.68.6-3-gabc1234" {
		t.Fatalf("runner policy saw %v, want only the team-pool launcher", checked)
	}
}

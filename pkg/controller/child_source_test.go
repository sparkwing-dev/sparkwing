package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

type childSourceEngine struct{ executable string }

func (e childSourceEngine) Resolve(_ context.Context, pin engine.Pin, target releaseasset.Target) (engine.Resolved, error) {
	return engine.Resolved{Identity: engine.Identity{Pin: pin, Version: pin.Version, ManifestDigest: "verified"}, Executable: e.executable, Target: target}, nil
}

func TestFreshChildRefLookupCannotSelectAnotherRepositoryOrDirectoryProperty(t *testing.T) {
	e, _, _ := childIntakeFixture(t)
	server := New(e.Store, nil)
	tenant, err := e.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	a := capability.Authorized{Target: capability.Target{Team: "a", RunID: "run", NodeID: "job"}}
	property := func(value []byte, mutation uint8) bool {
		request := parent.Request.Request
		request.Git.SHA, request.Git.Ref = "", "deploy"
		request.Lineage = runspec.Lineage{ParentRunID: "run", ParentJobID: "job"}
		encoded := base64.RawURLEncoding.EncodeToString(value)
		switch mutation % 3 {
		case 0:
			request.Source.Location = runspec.SourceLocation{RemoteURL: "https://8.8.8.8/" + encoded, PipelineDir: ".sparkwing"}
		case 1:
			request.Git.Repository.Key = runspec.RepoKey("foreign:" + encoded)
		case 2:
			request.Lineage.ParentJobID = "foreign:" + encoded
		}
		err := server.resolveChildRevisions(t.Context(), e, a, &request)
		var refusal *apierr.Error
		return errors.As(err, &refusal) && refusal.Code == apierr.Forbidden
	}
	if err := quick.Check(property, nil); err != nil {
		t.Fatal(err)
	}
}

func (e childSourceEngine) Ensure(ctx context.Context, identity engine.Identity, target releaseasset.Target) (engine.Resolved, error) {
	return e.Resolve(ctx, identity.Pin, target)
}

func TestNativeFreshBranchChildResolvesBeforeIntakeAndAcquisition(t *testing.T) {
	e, _, claim := childIntakeFixture(t)
	handler := New(e.Store, nil).WithEngineController(e).Handler()
	engineClaimHTTP(t, handler, claim, "POST", "/api/v1/runs/run/source-credential", "", http.StatusForbidden)
	repo, original := sourcetest.Repository(t)
	remote := "https://8.8.8.8/team/repo"
	sourcetest.Remote(t, map[string]string{remote: repo})
	sourcetest.Git(t, repo, "checkout", "-qb", "deploy")
	sourcetest.Write(t, filepath.Join(repo, ".sparkwing", "pipeline.go"), "package main // selected child\n")
	sourcetest.Write(t, filepath.Join(repo, ".sparkwing", "go.mod"), "module pipeline\n\ngo 1.26.0\n\nrequire github.com/sparkwing-dev/sparkwing v1.2.3\n")
	sourcetest.Git(t, repo, "add", ".")
	sourcetest.Git(t, repo, "commit", "-qm", "child")
	selected := sourcetest.Git(t, repo, "rev-parse", "HEAD")
	sourcetest.Git(t, repo, "checkout", "-q", "trunk")
	tenant, err := e.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	parent.Request.Request.Git.SHA = original
	parent.Request.Request.Git.Repository = runspec.Repository{Key: "git:8.8.8.8/team/repo", Display: remote}
	childSourceRepositories(t, e, claim, []string{"git:8.8.8.8/team/repo"})
	parent.Request.Request.Source = runspec.Source{Ref: original, Location: runspec.SourceLocation{RemoteURL: remote, PipelineDir: ".sparkwing"}}
	body, err := json.Marshal(parent.Request)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `UPDATE runs SET request_json=?,repo_key=? WHERE team='a' AND id='run'`, body, "git:8.8.8.8/team/repo")
	host, err := client.NewNodeHostClient(client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: childRecorderTransport{handler}}, Bearer: func(context.Context) (string, error) { return claim, nil }}, "run", "job")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	})
	principal, err := host.Principal(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	process, err := host.ProcessClient(t.Context(), principal, "job")
	if err != nil {
		t.Fatal(err)
	}
	id, err := process.StartChild(t.Context(), "deploy", nil, "", "deploy")
	if err != nil {
		t.Fatal(err)
	}
	child, err := tenant.ReadRun(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	request := child.Request.Request
	if request.Git.SHA != selected || request.Source.Ref != selected || request.Source.WorkingTree || !request.Source.CommittedCheckout || request.Source.BundleDigest.Set {
		t.Fatalf("child source was not resolved before sealing: %+v", request.Source)
	}
	sourcetest.Git(t, repo, "branch", "-f", "deploy", original)
	spec, err := source.ForRequest(child.Request)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	installed := false
	entry, err := source.Acquire(t.Context(), spec, source.Options{Workspace: t.TempDir(), Access: childSourcePublicAccess{}, Engines: childSourceEngine{executable}, Target: releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment, InstallSDK: func(_ context.Context, selectedEngine engine.Resolved, install engine.SDKInstallRequest) (engine.SDKCommand, error) {
		directory := filepath.Join(install.Directory, install.PipelineDir)
		if selectedEngine.Identity.Pin.SDK != engine.GoSDK || selectedEngine.Identity.Pin.Version != "v1.2.3" || sourcetest.Git(t, install.Directory, "rev-parse", "HEAD") != selected {
			t.Fatal("installer selected another branch or SDK")
		}
		installed = true
		return engine.SDKCommand{Directory: directory, Command: []string{executable}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !installed || entry.SHA != selected {
		t.Fatal("child did not reach shared acquisition")
	}
	if err := entry.Close(); err != nil {
		t.Fatal(err)
	}
}

type childSourcePublicAccess struct{}

func (childSourcePublicAccess) GitCredential(context.Context, source.Spec) (source.Credential, error) {
	return source.Credential{}, nil
}
func (childSourcePublicAccess) BundleURL(context.Context, string) (string, error) {
	return "", fmt.Errorf("unexpected bundle")
}

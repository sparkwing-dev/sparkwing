package controller

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func sourceUploadFixture(t *testing.T, opts ...store.Option) (*serverFixture, client.EngineOptions, runspec.Request, string) {
	t.Helper()
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: t.TempDir(), Endpoint: "http://controller", SigningKey: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	s, session := credentialSessionServerWith(t, func(s *Server) { s.WithSecretsCipher(cipher).WithDataObjects(objects) }, opts...)
	h := s.Handler()
	w := credentialRequestHTTP(t, h, session, `{"grants":["runs.write","runs.read","runs.control"]}`, false)
	var credential credentialResponse
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &credential) != nil {
		t.Fatalf("credential: %d %s", w.Code, w.Body)
	}
	options := client.EngineOptions{URL: "http://controller", HTTP: &http.Client{Transport: inventoryRoundTrip{h}}, Bearer: func(context.Context) (string, error) { return credential.Capability, nil }}
	input := runspec.Request{Version: 1, Pipeline: "ci", Mode: runspec.IntakeExecute, Origin: runspec.OriginTrigger, Dispatch: runspec.DispatchHost, Git: runspec.Git{Repository: runspec.Repository{Key: "git:example.test/acme/repo"}, SHA: strings.Repeat("a", 40), Ref: "main"}, Source: runspec.Source{Ref: strings.Repeat("a", 40), Entrypoint: "CI", Location: runspec.SourceLocation{RemoteURL: "https://example.test/acme/repo.git", PipelineDir: ".sparkwing"}}}
	return s, options, input, credential.Capability
}

func TestSourceSnapshotRealClientStorageAndIntake(t *testing.T) {
	s, options, input, raw := sourceUploadFixture(t)
	path := filepath.Join(t.TempDir(), "snapshot.bundle")
	repo, sha := sourcetest.Repository(t)
	sourcetest.Git(t, repo, "bundle", "create", path, "HEAD")
	input.Git.SHA, input.Source.Ref = sha, sha
	digest, err := client.UploadSourceSnapshot(t.Context(), options, source.Publication{Git: input.Git, Source: input.Source, Path: path, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t), Masker: secrets.NewMasker()}, Secrets: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	input.Source.BundleDigest = runspec.Optional[string]{Set: true, Value: digest}
	c, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	request := controllerwire.CreateRunRequest{IdempotencyKey: "snapshot", Request: input}
	created, err := c.CreateRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if created.Request.Request.Source.BundleDigest.Value != digest || created.Request.Request.Git.App.Set {
		t.Fatalf("source seal: %+v", created.Request)
	}
	replay, err := c.CreateRun(t.Context(), request)
	if err != nil || !replay.Replayed || replay.RunID != created.RunID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	var count int
	if err := teststore.SQL(t, s.store).QueryRow("SELECT COUNT(*) FROM blob_records WHERE team='a' AND run_id=? AND kind='source_bundle' AND provenance='local' AND verified_ref=0", created.RunID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("source roots=%d: %v", count, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	var used, objects int64
	if err := teststore.SQL(t, s.store).QueryRow("SELECT bytes,objects FROM storage_run_usage WHERE team='a' AND run_id=?", created.RunID).Scan(&used, &objects); err != nil || used != info.Size() || objects != 1 {
		t.Fatalf("run storage: bytes=%d objects=%d %v", used, objects, err)
	}
	runner := engineHTTPRoot(t, newEngineFixture(s.store, EngineController{}), "a", "snapshot-runner", []capability.Grant{capability.TriggersClaim})
	s.engine.claimProfiles = engineProfileFake{}
	claim, err := s.store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "snapshot-runner", Target: &store.UnitRef{Kind: capability.UnitPlan, RunID: runspec.RunID(created.RunID)}, Capacity: match.Resources{Cores: 1}, Available: match.Resources{Cores: 1}})
	if err != nil || len(claim.Bearer) == 0 {
		t.Fatalf("claim: %+v %v", claim, err)
	}
	awarded := options
	awarded.Bearer = func(context.Context) (string, error) { return string(claim.Bearer), nil }
	spec := source.Spec{RepoKey: string(input.Git.Repository.Key), RemoteURL: input.Source.Location.RemoteURL, SHA: input.Source.Ref, PipelineDir: ".sparkwing", BundleDigest: digest}
	access, err := client.NewSourceAccess(awarded, created.RunID, spec)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := source.PrepareCheckout(t.Context(), spec, source.Options{Workspace: t.TempDir(), Access: access, BundleTransport: options.HTTP.Transport, BuildEnvironment: sourcetest.BuildEnvironment})
	if err != nil {
		t.Fatal(err)
	}
	if entry.SHA != sha {
		t.Fatalf("bundle HEAD=%s, want %s", entry.SHA, sha)
	}
	if err := entry.Close(); err != nil {
		t.Fatal(err)
	}
	if err := teststore.SQL(t, s.store).QueryRow(`SELECT COUNT(*) FROM audit_events WHERE kind='git_credential.released' AND team='a' AND run_id=?`, created.RunID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("complete bundle released a source credential: audits=%d: %v", count, err)
	}
	engineClaimHTTP(t, s.Handler(), runner, "POST", "/api/v1/data/download", `{"digest":"`+digest+`"}`, 403)
	request.IdempotencyKey = "reuse"
	if _, err := c.CreateRun(t.Context(), request); err == nil {
		t.Fatal("consumed snapshot accepted twice")
	}
	if _, err := client.UploadSourceSnapshot(t.Context(), options, source.Publication{Git: input.Git, Source: input.Source, Path: path, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t), Masker: secrets.NewMasker()}, Secrets: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	request.IdempotencyKey = "deduplicated"
	second, err := c.CreateRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := teststore.SQL(t, s.store).QueryRow("SELECT COUNT(*) FROM storage_run_usage WHERE team='a' AND run_id=?", second.RunID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("duplicate source charged run: %d %v", count, err)
	}
	engineClaimHTTP(t, s.Handler(), raw, "POST", "/api/v1/data/download", `{"digest":"`+digest+`"}`, 403)
}

func TestSourceSnapshotAuthorityAndRevisionProperty(t *testing.T) {
	s, options, input, raw := sourceUploadFixture(t)
	path := filepath.Join(t.TempDir(), "snapshot.bundle")
	if err := os.WriteFile(path, []byte("snapshot bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := client.UploadSourceSnapshot(t.Context(), options, source.Publication{Git: input.Git, Source: input.Source, Path: path, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t), Masker: secrets.NewMasker()}, Secrets: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	input.Source.BundleDigest = runspec.Optional[string]{Set: true, Value: digest}
	h := s.Handler()
	property := func(seed []byte, which uint8) bool {
		forged := input
		sum := fmt.Sprintf("%x", sha256.Sum256(seed))
		switch which % 4 {
		case 0:
			forged.Git.SHA = sum[:40]
		case 1:
			forged.Source.Ref = sum[:40]
		case 2:
			forged.Source.BundleDigest.Value = sum
		case 3:
			forged.Git.Repository.Key = runspec.RepoKey("git:example.test/foreign/" + sum)
			forged.Source.Location.RemoteURL = "https://example.test/foreign/" + sum
		}
		body, err := json.Marshal(controllerwire.CreateRunRequest{IdempotencyKey: sum, Request: forged})
		if err != nil {
			return false
		}
		w := sourceUploadHTTP(h, raw, "POST", "/api/v1/runs", string(body))
		return w.Code == 403
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
	engineSQL(t, s.store, `DELETE FROM memberships WHERE team='a' AND account_id='member'`)
	if _, err := client.UploadSourceSnapshot(t.Context(), options, source.Publication{Git: input.Git, Source: input.Source, Path: path, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t), Masker: secrets.NewMasker()}, Secrets: map[string]string{}}); err == nil {
		t.Fatal("revoked member uploaded source")
	}
}

func sourceUploadHTTP(h http.Handler, raw, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+raw)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestSourceSnapshotRevokedBeforeCommitExpiresPhysicalStaging(t *testing.T) {
	s, options, input, _ := sourceUploadFixture(t)
	directory := t.TempDir()
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: directory, Endpoint: "http://controller", SigningKey: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	s.WithDataObjects(objects)
	transport := inventoryRoundTrip{s.Handler()}
	options.HTTP.Transport = intakeGitHubTransport(func(r *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(r)
		if err == nil && r.Method == "PUT" && strings.Contains(r.URL.Path, "/data/uploads/") {
			engineSQL(t, s.store, `DELETE FROM memberships WHERE team='a' AND account_id='member'`)
		}
		return response, err
	})
	path := filepath.Join(t.TempDir(), "snapshot.bundle")
	if err := os.WriteFile(path, []byte("private snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UploadSourceSnapshot(t.Context(), options, source.Publication{Git: input.Git, Source: input.Source, Path: path, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t), Masker: secrets.NewMasker()}, Secrets: map[string]string{}}); err == nil {
		t.Fatal("revoked member committed source")
	}
	var id string
	if err := teststore.SQL(t, s.store).QueryRow("SELECT id FROM source_uploads WHERE committed_at=0").Scan(&id); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(directory, "teams", "a", "pending", id)
	if _, err := os.Stat(stage); err != nil {
		t.Fatalf("fixture did not stage source: %v", err)
	}
	engineSQL(t, s.store, "UPDATE source_uploads SET expires_at=1 WHERE id=?", id)
	engineSQL(t, s.store, "UPDATE quota_reservations SET expires_at=1 WHERE id=?", "upload:"+id)
	lease, err := s.store.AsOperator().AcquireSingletonLease(t.Context(), "maintenance", "source-expiry", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.maintainDataObjects(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("staged source survived expiry: %v", err)
	}
	var state string
	if err := teststore.SQL(t, s.store).QueryRow("SELECT state FROM quota_reservations WHERE id=?", "upload:"+id).Scan(&state); err != nil || state != "released" {
		t.Fatalf("source reservation state=%s %v", state, err)
	}
}

type failedSourceSigner struct{ blob.ObjectBackend }

func (failedSourceSigner) Sign(context.Context, capability.ObjectToSign) (capability.SignedObject, error) {
	return capability.SignedObject{}, errors.New("signing failed")
}

func TestSourceSnapshotSigningFailureReleasesReservation(t *testing.T) {
	s, options, input, _ := sourceUploadFixture(t)
	s.WithDataObjects(failedSourceSigner{s.dataController.Objects})
	path := filepath.Join(t.TempDir(), "snapshot.bundle")
	if err := os.WriteFile(path, []byte("snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UploadSourceSnapshot(t.Context(), options, source.Publication{Git: input.Git, Source: input.Source, Path: path, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t), Masker: secrets.NewMasker()}, Secrets: map[string]string{}}); err == nil {
		t.Fatal("signer failure was hidden")
	}
	var reserved, count int64
	if err := teststore.SQL(t, s.store).QueryRow("SELECT reserved FROM quota_admission WHERE team='a' AND quota='storage'").Scan(&reserved); err != nil || reserved != 0 {
		t.Fatalf("failed signing held quota=%d %v", reserved, err)
	}
	if err := teststore.SQL(t, s.store).QueryRow("SELECT COUNT(*) FROM source_uploads").Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed signing retained tickets=%d %v", count, err)
	}
}

func TestSourceSnapshotPublishesScannedCopyAfterInputChanges(t *testing.T) {
	_, options, input, _ := sourceUploadFixture(t)
	repo, sha := sourcetest.Repository(t)
	path := filepath.Join(t.TempDir(), "source.bundle")
	sourcetest.Git(t, repo, "bundle", "create", path, "HEAD")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	input.Git.SHA, input.Source.Ref = sha, sha
	transport := options.HTTP.Transport
	changed := false
	options.HTTP.Transport = intakeGitHubTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" && r.URL.Path == "/api/v1/data/source" {
			if err := os.WriteFile(path, []byte("private-value-added-after-scan"), 0o600); err != nil {
				return nil, err
			}
			changed = true
		}
		return transport.RoundTrip(r)
	})
	digest, err := client.UploadSourceSnapshot(t.Context(), options, source.Publication{Git: input.Git, Source: input.Source, Path: path, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t), Masker: secrets.NewMasker()}, Secrets: map[string]string{"SIGNING_MATERIAL": "private-value-added-after-scan"}})
	if err != nil || !changed || digest != fmt.Sprintf("%x", sha256.Sum256(original)) {
		t.Fatalf("upload differed from scanned source: changed=%t digest=%s err=%v", changed, digest, err)
	}
}

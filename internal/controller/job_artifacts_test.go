package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestJobArtifactsPublishesListsAndDownloadsUnderCurrentMembership(t *testing.T) {
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: t.TempDir(), Endpoint: "http://local", SigningKey: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	e, tenant := engineServerFixture(t, deployment.Config{}, func(s *Server) { s.WithDataObjects(objects) })
	award := engineRuntimeClaim(t, e, capability.UnitNode)
	raw := nativeClaimPresentation(t, e, award)
	h := e.Server.Handler()
	engineSQL(t, e.Store, `UPDATE nodes SET spec_json=? WHERE team='a' AND run_id='run' AND node_id='job'`, []byte(`{"id":"job","artifacts":["dist/**"]}`))
	engineAdmitBearer(t, e.Store, raw)
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/runs/run/jobs/job/transition", `{"to":"running","cause":"start","source_attempt":0}`, 200)
	authority, err := tenant.ResolveBlobAuthority(t.Context(), blob.Authority{CapabilityRef: award.Principal.Capability.Prefix}, capability.DataRead)
	if err != nil {
		t.Fatal(err)
	}
	file := dataResultUpload(t, tenant, objects, authority, authority.Upload.Value, "artifact-file", []byte("published file\n"))
	entry := blob.ManifestEntry{Path: "dist/file.txt", Digest: file.Digest, Size: file.Size, Mode: 0o644}
	body, err := json.Marshal(blob.Manifest{Entries: []blob.ManifestEntry{entry}})
	if err != nil {
		t.Fatal(err)
	}
	manifest := dataResultUpload(t, tenant, objects, authority, authority.Upload.Value, "artifact-manifest", body)
	key := blob.ResultRecordKey(authority.Attempt.Value, authority.Repo, authority.Ref.Scope, "ci", blob.KindManifest)
	if _, err := tenant.CommitRecord(t.Context(), blob.CommitRecordRequest{Authority: authority, Source: authority.Upload.Value, Key: key, Digest: manifest.Digest, Members: []blob.Member{{Team: "a", ManifestDigest: manifest.Digest, Path: entry.Path, Digest: entry.Digest, Size: entry.Size, Mode: entry.Mode}}}); err != nil {
		t.Fatal(err)
	}
	options, _, server := memberObservationOptions(t, e.Store, objects)
	engineClaimHTTP(t, server.Handler(), raw, "GET", "/api/v1/runs/run/jobs/job/artifacts", "", http.StatusForbidden)
	cacheRaw := engineHTTPRootAs(t, e, capability.SubjectAccount, "a", "member", []capability.Grant{capability.DataRead})
	cachePrincipal, err := e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(cacheRaw)))
	if err != nil {
		t.Fatal(err)
	}
	cacheCapability := cachePrincipal.Capability
	cacheCapability.Data = &capability.DataScope{Use: capability.DataRemoteCache, RepositoryKey: "gh:1", ReadRefs: []string{"main"}, VerifiedDefaultOnly: true}
	cacheEnvelope, err := json.Marshal(store.CapabilityRecord{Version: 1, Value: cacheCapability})
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `UPDATE capabilities SET authority_json=? WHERE prefix='member'`, cacheEnvelope)
	cacheOptions := options
	cacheOptions.Bearer = func(context.Context) (string, error) { return cacheRaw, nil }
	cacheReader, err := client.NewArtifactReader(cacheOptions)
	if err != nil {
		t.Fatal(err)
	}
	if entries, err := cacheReader.ReadArtifacts(t.Context(), "run", "job"); err == nil || len(entries) != 0 {
		t.Fatalf("cache credential read artifacts = %+v, %v", entries, err)
	}

	reader, err := client.NewArtifactReader(options)
	if err != nil {
		t.Fatal(err)
	}
	if entries, err := reader.ReadArtifacts(t.Context(), "run", "job"); err != nil || len(entries) != 0 {
		t.Fatalf("uncommitted files = %+v, %v", entries, err)
	}
	finished, err := json.Marshal(controllerwire.JobTransitionRequest{To: "success", Cause: "report", SourceAttempt: new(int64(1)), Result: &controllerwire.JobResult{Outcome: "success", Manifest: &key}})
	if err != nil {
		t.Fatal(err)
	}
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/runs/run/jobs/job/transition", string(finished), 200)
	entries, err := reader.ReadArtifacts(t.Context(), "1", "job")
	if err != nil || len(entries) != 1 || entries[0] != entry {
		t.Fatalf("artifact listing = %+v, %v", entries, err)
	}
	var downloaded bytes.Buffer
	got, err := reader.DownloadArtifact(t.Context(), "run", "job", entry.Path, &downloaded)
	if err != nil || got != entry || downloaded.String() != "published file\n" {
		t.Fatalf("artifact download = %+v, %q, %v", got, downloaded.String(), err)
	}
	if _, err := reader.DownloadArtifact(t.Context(), "run", "job", "../other", &downloaded); err == nil {
		t.Fatal("read a path outside the manifest")
	}

	engineSQL(t, e.Store, `INSERT INTO teams(name,created_at,updated_at) VALUES ('b',1,1)`)
	engineSQL(t, e.Store, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('foreign-artifact-reader','foreign@example.test',1,1)`)
	engineSQL(t, e.Store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('b','foreign-artifact-reader','editor',1)`)
	foreign := engineHTTPRootAs(t, e, capability.SubjectAccount, "b", "foreign-artifact-reader", []capability.Grant{capability.RunsRead})
	engineClaimHTTP(t, server.Handler(), foreign, "GET", "/api/v1/runs/run/jobs/job/artifacts", "", http.StatusNotFound)
	if err := objects.DeleteBlob(t.Context(), file); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.DownloadArtifact(t.Context(), "run", "job", entry.Path, &downloaded); err == nil {
		t.Fatal("missing artifact bytes succeeded")
	}
	engineSQL(t, e.Store, `UPDATE blob_records SET expires_at=1 WHERE team='a' AND kind='manifest'`)
	if entries, err := reader.ReadArtifacts(t.Context(), "run", "job"); err != nil || len(entries) != 0 {
		t.Fatalf("expired files = %+v, %v", entries, err)
	}
	engineSQL(t, e.Store, `DELETE FROM memberships WHERE team='a' AND account_id='member'`)
	if entries, err := reader.ReadArtifacts(t.Context(), "run", "job"); err == nil || len(entries) != 0 {
		t.Fatalf("revoked membership read files = %+v, %v", entries, err)
	}
}

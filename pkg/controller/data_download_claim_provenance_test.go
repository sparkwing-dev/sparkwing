package controller

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestDataDownloadClaimUsesCloudProvenance(t *testing.T) {
	srv, _, head := downloadFixture(t)
	srv.WithTeamDownloadCaps(1<<20, 1<<20)
	client := s3.NewFromConfig(aws.Config{Region: "us-west-2", Credentials: credentials.NewStaticCredentialsProvider("FIXTURE", "FIXTURE", "")})
	srv.WithDirectUploads(client, "bucket", "cache")
	st, ctx, now := srv.store, t.Context(), time.Now()
	commit := func(key, digest, provenance, repo, ref, run string) {
		t.Helper()
		u, err := st.ReserveUpload(ctx, store.UploadRequest{
			Team: store.DefaultTeam, RunID: run, Kind: store.StorageCache, Key: key, Size: 4,
			SHA256: digest, Principal: "fixture", ClaimPrefix: "swu_fixture", Provenance: provenance, Repo: repo, Ref: ref,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.CommitUpload(ctx, u.Team, u.ID, u.Principal, now); err != nil {
			t.Fatal(err)
		}
	}
	digest := strings.Repeat("a", 64)
	source := "sources/" + digest + "/" + strings.Repeat("1", 32)
	commit(source, digest, "local", "", "", "")
	tenant, err := st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	if err := tenant.CreateSourceTriggerWithRun(ctx,
		store.Trigger{
			ID: "run-d", Pipeline: "demo", CreatedAt: now, GitBranch: "main", GithubRepoID: 42,
			TriggerSource: "pipeline-working-tree@fixture", TriggerEnv: map[string]string{bincache.SourceBundleObjectEnvKey: source},
		},
		store.Run{ID: "run-d", Pipeline: "demo", Status: "pending", StartedAt: now}, source, "fixture", "swu_fixture"); err != nil {
		t.Fatal(err)
	}
	if err := st.CreatePlanNode(ctx, store.DefaultTeam, "run-d", now); err != nil {
		t.Fatal(err)
	}
	f := dispatchRouteFixture{st: st, handler: srv.EnableAuthFromStore().Handler()}
	plan := f.claim(t, store.PlanNodeID, store.ClaimTokenPlan)
	wantPost(t, f, planPath, plan, `{"nodes":[{"id":"a","deps":[],`+dispatchHash+`}]}`, http.StatusOK, "accepted")
	work := f.claim(t, "a", store.ClaimTokenWork)
	code, raw := f.do(t, http.MethodPost, "/api/v1/runs/run-d/cache-grant", work, []byte(`{}`))
	if code != http.StatusOK {
		t.Fatalf("cache grant = %d %s", code, raw)
	}
	var grant CacheGrantResponse
	if err := json.Unmarshal(raw, &grant); err != nil {
		t.Fatal(err)
	}
	for _, object := range []struct{ key, digest, provenance, repo, ref string }{
		{"artifacts/blobs/" + digest, digest, "local", "", ""},
		{"artifacts/blobs/" + strings.Repeat("b", 64), strings.Repeat("b", 64), "cloud", "", ""},
		{"bin/01234567-89abcdef/" + digest, digest, "local", "github:42", "refs/heads/main"},
		{"bin/01234567-89abcdee/" + digest, digest, "cloud", "github:42", "refs/heads/main"},
	} {
		commit(object.key, object.digest, object.provenance, object.repo, object.ref, "run-d")
	}
	for _, tc := range []struct {
		name, kind, key string
		want            int
	}{
		{"local artifact", "artifact", "artifacts/blobs/" + digest, http.StatusNotFound},
		{"cloud artifact", "artifact", "artifacts/blobs/" + strings.Repeat("b", 64), http.StatusOK},
		{"local binary", "binary", "bin/01234567-89abcdef", http.StatusNotFound},
		{"cloud binary", "binary", "bin/01234567-89abcdee", http.StatusOK},
		{"legacy binary", "binary", "bins/fixture", http.StatusNotFound},
		{"bound source", "source", source, http.StatusOK},
		{"unbound source", "source", "sources/" + digest + "/" + strings.Repeat("2", 32), http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(dataDownloadRequest{Kind: tc.kind, Key: tc.key})
			code, _ := f.do(t, http.MethodPost, "/api/v1/data/download", grant.Grant, body)
			if code != tc.want {
				t.Fatalf("download = %d, want %d", code, tc.want)
			}
		})
	}
	if len(head.keys) != 0 {
		t.Fatalf("legacy storage accessed %d times", len(head.keys))
	}
	if err := st.SetTrustLocalBuilds(ctx, store.DefaultTeam, true); err != nil {
		t.Fatal(err)
	}
	for _, request := range []dataDownloadRequest{
		{Kind: "artifact", Key: "artifacts/blobs/" + digest}, {Kind: "binary", Key: "bin/01234567-89abcdef"},
	} {
		body, _ := json.Marshal(request)
		if code, _ := f.do(t, http.MethodPost, "/api/v1/data/download", grant.Grant, body); code != http.StatusOK {
			t.Fatalf("owner-trusted local %s = %d", request.Kind, code)
		}
	}
}

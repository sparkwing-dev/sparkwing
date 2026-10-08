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

	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestDataDownloadPreservesClaimOutputBoundary(t *testing.T) {
	f := newDispatchRouteFixture(t)
	plan := f.claim(t, store.PlanNodeID, store.ClaimTokenPlan)
	wantPost(t, f, planPath, plan, `{"nodes":[{"id":"a","deps":[],`+dispatchHash+`},{"id":"b","deps":[],`+dispatchHash+`}]}`, http.StatusOK, "accepted")
	a := f.claim(t, "a", store.ClaimTokenWork)
	ref := f.uploadOutput(t, "run-d", "a", a, []byte(`{"private":"a"}`))
	f.report(t, "a", a, ref)
	b := f.claim(t, "b", store.ClaimTokenWork)
	if code, raw := f.do(t, http.MethodGet, "/api/v1/runs/run-d/nodes/a/output", b, nil); code != http.StatusForbidden {
		t.Fatalf("nondependency output = %d %s", code, raw)
	}

	head := &downloadHead{}
	bucket, err := teamblob.New(teamblob.Options{Bucket: "private-fixture", Prefix: "cache", Client: head})
	if err != nil {
		t.Fatal(err)
	}
	client := s3.NewFromConfig(aws.Config{Region: "us-west-2", Credentials: credentials.NewStaticCredentialsProvider("FIXTURE", "FIXTURE", "")})
	srv := New(f.st, nil).EnableAuthFromStore().WithCacheGrantKey("private-fixture-key")
	if err := srv.WithSignedDownloads(bucket, nil, client, "", "", ""); err != nil {
		t.Fatal(err)
	}
	f.handler = srv.Handler()
	code, raw := f.do(t, http.MethodPost, "/api/v1/runs/run-d/cache-grant", b, []byte(`{}`))
	if code != http.StatusOK {
		t.Fatalf("cache grant = %d %s", code, raw)
	}
	var grant CacheGrantResponse
	if err := json.Unmarshal(raw, &grant); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"cloud/" + ref.Key, "local/" + ref.Key, "local/sources/private", "cloud/bin/private"} {
		t.Run(key, func(t *testing.T) {
			body, _ := json.Marshal(dataDownloadRequest{Kind: "artifact", Key: key})
			code, _ := f.do(t, http.MethodPost, "/api/v1/data/download", grant.Grant, body)
			if code != http.StatusBadRequest || len(head.keys) != 0 {
				t.Fatalf("non-artifact download = %d, object HEAD count = %d; want 400 before object access", code, len(head.keys))
			}
		})
	}
}

func TestDataDownloadSharesCommittedArtifactsWithinTheTeam(t *testing.T) {
	srv, grant, head := downloadFixture(t)
	tenant, err := srv.store.ForTeam(t.Context(), "team-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := tenant.CreateRun(t.Context(), store.Run{ID: "producer", Pipeline: "producer", Status: "success", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("b", 64)
	key := "artifacts/blobs/" + digest
	upload, err := srv.store.ReserveUpload(t.Context(), store.UploadRequest{
		Team: "team-a", RunID: "producer", Kind: store.StorageCache, Key: key,
		Size: 4, SHA256: digest, Principal: "producer", Provenance: "local",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.store.CommitUpload(t.Context(), "team-a", upload.ID, upload.Principal, time.Now()); err != nil {
		t.Fatal(err)
	}
	f := dispatchRouteFixture{handler: srv.Handler()}
	body, _ := json.Marshal(dataDownloadRequest{Kind: "artifact", Key: key})
	code, raw := f.do(t, http.MethodPost, "/api/v1/data/download", grant, body)
	if code != http.StatusOK {
		t.Fatalf("same-team artifact from another run = %d %s", code, raw)
	}
	var signed DataDownloadResponse
	if err := json.Unmarshal(raw, &signed); err != nil {
		t.Fatal(err)
	}
	if signed.SHA256 != digest || signed.Size != 4 || !strings.Contains(signed.URL, "/cache/teams/team-a/local/"+key) || len(head.keys) != 0 {
		t.Fatalf("artifact metadata or signing path differs: size=%d digest=%s HEAD count=%d", signed.Size, signed.SHA256, len(head.keys))
	}
}

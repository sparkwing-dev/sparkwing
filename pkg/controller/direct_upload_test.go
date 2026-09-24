package controller_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type directObject struct {
	body     []byte
	checksum string
	metadata map[string]string
}

func directS3(t *testing.T) (*s3.Client, func() map[string]directObject) {
	t.Helper()
	var mu sync.Mutex
	objects := map[string]directObject{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/bucket/")
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			if source := r.Header.Get("x-amz-copy-source"); source != "" {
				if r.Header.Get("If-None-Match") == "*" {
					if _, exists := objects[key]; exists {
						w.WriteHeader(http.StatusPreconditionFailed)
						return
					}
				}
				decoded, _ := url.PathUnescape(strings.TrimPrefix(source, "/"))
				decoded = strings.TrimPrefix(decoded, "bucket/")
				obj, ok := objects[decoded]
				if !ok {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				obj.metadata = map[string]string{}
				for k, v := range r.Header {
					if strings.HasPrefix(strings.ToLower(k), "x-amz-meta-") {
						obj.metadata[strings.TrimPrefix(strings.ToLower(k), "x-amz-meta-")] = v[0]
					}
				}
				objects[key] = obj
				w.Header().Set("Content-Type", "application/xml")
				_, _ = io.WriteString(w, "<CopyObjectResult><ETag>\"copied\"</ETag></CopyObjectResult>")
				return
			}
			body, _ := io.ReadAll(r.Body)
			objects[key] = directObject{body: body, checksum: r.Header.Get("x-amz-checksum-sha256")}
		case http.MethodHead:
			obj, ok := objects[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Length", stringLength(len(obj.body)))
			w.Header().Set("x-amz-checksum-sha256", obj.checksum)
			for k, v := range obj.metadata {
				w.Header().Set("x-amz-meta-"+k, v)
			}
		case http.MethodDelete:
			delete(objects, key)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	client := s3.New(s3.Options{
		Region: "us-west-2", BaseEndpoint: aws.String(srv.URL), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
	})
	return client, func() map[string]directObject {
		mu.Lock()
		defer mu.Unlock()
		clone := make(map[string]directObject, len(objects))
		for k, v := range objects {
			clone[k] = v
		}
		return clone
	}
}

func stringLength(n int) string { return strconv.Itoa(n) }

func claimedUploadGrant(t *testing.T, f *appFixture, team, runID, prefix string) string {
	t.Helper()
	t.Setenv(authwire.CacheGrantKeyEnv, "direct-test-grant-key")
	if _, err := f.store.DB().ExecContext(t.Context(), `UPDATE runs SET status = 'running' WHERE id = ?`, runID); err != nil {
		t.Fatal(err)
	}
	var claim authwire.CacheClaim
	var tokenPrefix string
	claim.Kind, claim.NodeID = "node", "compile"
	err := f.store.DB().QueryRowContext(t.Context(), `SELECT claim_principal, claim_token_prefix, claimed_by,
        COALESCE(claim_membership_id, ''), COALESCE(reservation_id, ''), claim_generation
        FROM nodes WHERE run_id = ? AND node_id = ?`, runID, claim.NodeID).Scan(
		&claim.Principal, &tokenPrefix, &claim.HolderID, &claim.MembershipID, &claim.ReservationID, &claim.Generation)
	if err != nil || tokenPrefix != prefix {
		t.Fatalf("node claim = %+v prefix=%q err=%v", claim, tokenPrefix, err)
	}
	claim.TokenPrefix = tokenPrefix
	grant, err := authwire.MintClaimCacheGrant("direct-test-grant-key", team, runID, time.Now(), time.Hour, &claim)
	if err != nil {
		t.Fatal(err)
	}
	return "Bearer " + grant
}

func TestPendingTriggerCanCommitBinaryCacheUpload(t *testing.T) {
	client, _ := directS3(t)
	f := newAppFixture(t, func(s *controller.Server) *controller.Server {
		return s.WithDirectUploads(client, "bucket", "cache")
	})
	owner := f.ghUser(655, "trigger-owner")
	_, prefix := f.runWork(owner, "run-pending-cache", "https://github.com/acme/widgets.git")
	token, err := f.store.LookupTokenByPrefix(prefix)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB().ExecContext(t.Context(), `UPDATE runs SET status = 'pending' WHERE id = ?`, "run-pending-cache"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB().ExecContext(t.Context(), `UPDATE triggers SET status = 'claimed', claim_principal = ?, claim_token_prefix = ?, claim_seq = 1, lease_expires_at = ? WHERE id = ?`,
		token.Principal, prefix, time.Now().Add(time.Hour).UnixNano(), "run-pending-cache"); err != nil {
		t.Fatal(err)
	}
	t.Setenv(authwire.CacheGrantKeyEnv, "direct-test-grant-key")
	grant, err := authwire.MintClaimCacheGrant("direct-test-grant-key", owner.team, "run-pending-cache", time.Now(), time.Hour,
		&authwire.CacheClaim{Kind: "trigger", Generation: 1, Principal: token.Principal, TokenPrefix: prefix})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("compiled binary")
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	key := "bin/01234567-89abcdef/" + digest
	var reserved controller.DirectUploadResponse
	if code := f.call("POST", "/api/v1/data/upload", "Bearer "+grant,
		map[string]any{"kind": "binary", "key": key, "size": len(body), "sha256": digest, "run_id": "run-pending-cache"}, &reserved); code != http.StatusOK {
		t.Fatalf("pending trigger reserve = %d", code)
	}
	if _, err := client.PutObject(t.Context(), &s3.PutObjectInput{
		Bucket: aws.String("bucket"), Key: aws.String("pending/" + reserved.UploadID),
		Body: bytes.NewReader(body), ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(sum[:])),
	}); err != nil {
		t.Fatal(err)
	}
	if code := f.call("POST", "/api/v1/data/commit", "Bearer "+grant,
		map[string]any{"upload_id": reserved.UploadID, "run_id": "run-pending-cache"}, nil); code != http.StatusNoContent {
		t.Fatalf("pending trigger commit = %d", code)
	}
}

func TestDirectUploadChecksumAndVisibility(t *testing.T) {
	client, objects := directS3(t)
	f := newAppFixture(t, func(s *controller.Server) *controller.Server {
		return s.WithDirectUploads(client, "bucket", "cache")
	})
	olga := f.ghUser(521, "olga")
	rawRunner, prefix := f.runWork(olga, "run-bytes", "https://github.com/acme/widgets.git")
	var services controller.ServicesResponse
	if code := f.call("GET", "/api/v1/services", rawRunner, nil, &services); code != http.StatusOK || !services.DirectData {
		t.Fatalf("direct data announcement = %d %+v", code, services)
	}
	if err := f.store.SetTokenMetered(t.Context(), prefix, true); err != nil {
		t.Fatal(err)
	}
	runner := claimedUploadGrant(t, f, olga.team, "run-bytes", prefix)
	body := []byte("hello direct upload")
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	key := "bin/01234567-89abcdef/" + digest
	request := map[string]any{
		"kind": "binary", "key": "bin/01234567-89abcdef/" + digest, "size": len(body),
		"sha256": digest, "run_id": "run-bytes",
	}
	var answer controller.DirectUploadResponse
	if code := f.call("POST", "/api/v1/data/upload", runner, request, &answer); code != http.StatusOK {
		t.Fatalf("reserve = %d", code)
	}
	if answer.UploadID == "" || answer.URL == "" {
		t.Fatalf("reserve = %+v", answer)
	}
	parsed, err := url.Parse(answer.URL)
	if err != nil {
		t.Fatal(err)
	}
	signed := parsed.Query().Get("X-Amz-SignedHeaders")
	if !strings.Contains(signed, "content-length") || !strings.Contains(signed, "x-amz-checksum-sha256") {
		t.Fatalf("signed headers = %q; want length and checksum", signed)
	}
	if _, ok := objects()["cache/teams/"+olga.team+"/cloud/"+key]; ok {
		t.Fatal("uncommitted object became visible")
	}
	if _, err := f.store.CommittedObject(t.Context(), store.Team(olga.team), key); err == nil {
		t.Fatal("uncommitted row became visible")
	}
	put, err := http.NewRequest(http.MethodPut, answer.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range answer.Headers {
		put.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(put)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT = %d", resp.StatusCode)
	}
	// safety: The fake accepts a forged checksum; the controller must catch it when it HEADs the pending object.
	_, err = client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String("bucket"),
		Key:    aws.String("pending/" + answer.UploadID), Body: bytes.NewReader(body), ChecksumSHA256: aws.String("d3Jvbmc="),
	})
	if err != nil {
		t.Fatal(err)
	}
	var ignored map[string]any
	if code := f.call("POST", "/api/v1/data/commit", runner, map[string]any{"upload_id": answer.UploadID, "run_id": "run-bytes"}, &ignored); code != http.StatusUnprocessableEntity {
		t.Fatalf("checksum mismatch commit = %d, want 422", code)
	}
	if _, ok := objects()["cache/teams/"+olga.team+"/cloud/"+key]; ok {
		t.Fatal("mismatched object became visible")
	}
	_, err = client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String("bucket"),
		Key:    aws.String("pending/" + answer.UploadID), Body: bytes.NewReader(body),
		ChecksumSHA256: aws.String(answer.Headers["x-amz-checksum-sha256"]),
	})
	if err != nil {
		t.Fatal(err)
	}
	if code := f.call("POST", "/api/v1/data/commit", runner, map[string]any{"upload_id": answer.UploadID, "run_id": "run-bytes"}, &ignored); code != http.StatusNoContent {
		t.Fatalf("valid commit = %d, want 204; logs: %s", code, f.logs.String())
	}
	final := objects()["cache/teams/"+olga.team+"/cloud/"+key]
	if !bytes.Equal(final.body, body) || final.metadata["uploader"] == "" || final.metadata["provenance"] != "cloud" {
		t.Fatalf("committed object = %+v", final)
	}
	committed, err := f.store.CommittedObject(t.Context(), store.Team(olga.team), key)
	if err != nil || committed.Provenance != "cloud" || committed.Principal == "" {
		t.Fatalf("committed row = %+v, %v", committed, err)
	}
}

func TestDirectCommitAdoptsVerifiedCopyAfterOldReservationExpires(t *testing.T) {
	client, _ := directS3(t)
	f := newAppFixture(t, func(s *controller.Server) *controller.Server {
		return s.WithDirectUploads(client, "bucket", "cache")
	})
	olga := f.ghUser(522, "olga")
	_, prefix := f.runWork(olga, "run-retry", "https://github.com/acme/widgets.git")
	if err := f.store.SetTokenMetered(t.Context(), prefix, true); err != nil {
		t.Fatal(err)
	}
	runner := claimedUploadGrant(t, f, olga.team, "run-retry", prefix)
	body := []byte("retry copy")
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	key := "artifacts/blobs/" + digest
	var answer controller.DirectUploadResponse
	if code := f.call("POST", "/api/v1/data/upload", runner, map[string]any{
		"kind": "artifact", "key": key, "size": len(body), "sha256": digest, "run_id": "run-retry",
	}, &answer); code != http.StatusOK {
		t.Fatalf("reserve = %d", code)
	}
	_, err := client.PutObject(t.Context(), &s3.PutObjectInput{
		Bucket: aws.String("bucket"),
		Key:    aws.String("pending/" + answer.UploadID), Body: bytes.NewReader(body),
		ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(sum[:])),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CopyObject(t.Context(), &s3.CopyObjectInput{
		Bucket:            aws.String("bucket"),
		Key:               aws.String("cache/teams/" + olga.team + "/cloud/" + key),
		CopySource:        aws.String(url.PathEscape("bucket/pending/" + answer.UploadID)),
		MetadataDirective: types.MetadataDirectiveReplace,
		Metadata:          map[string]string{"upload-id": answer.UploadID, "sha256": digest, "provenance": "cloud", "uploader": "previous-team-member"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CommittedObject(t.Context(), store.Team(olga.team), key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("copied but uncommitted row = %v", err)
	}
	if _, err := f.store.ReleaseExpiredStorage(t.Context(), time.Now().Add(store.DirectUploadTTL+time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.PruneExpiredUploads(t.Context(), time.Now().Add(store.DirectUploadTTL+time.Hour)); err != nil {
		t.Fatal(err)
	}
	var retry controller.DirectUploadResponse
	if code := f.call("POST", "/api/v1/data/upload", runner, map[string]any{
		"kind": "artifact", "key": key, "size": len(body), "sha256": digest, "run_id": "run-retry",
	}, &retry); code != http.StatusOK || retry.UploadID == answer.UploadID {
		t.Fatalf("new reservation = %d %+v", code, retry)
	}
	_, err = client.PutObject(t.Context(), &s3.PutObjectInput{
		Bucket: aws.String("bucket"),
		Key:    aws.String("pending/" + retry.UploadID), Body: bytes.NewReader(body),
		ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(sum[:])),
	})
	if err != nil {
		t.Fatal(err)
	}
	var ignored map[string]any
	if code := f.call("POST", "/api/v1/data/commit", runner, map[string]any{
		"upload_id": retry.UploadID, "run_id": "run-retry",
	}, &ignored); code != http.StatusNoContent {
		t.Fatalf("retry commit = %d, logs: %s", code, f.logs.String())
	}
	committed, err := f.store.CommittedObject(t.Context(), store.Team(olga.team), key)
	if err != nil || committed.Principal != "previous-team-member" {
		t.Fatalf("committed uploader = %+v, %v", committed, err)
	}
	reservation, err := f.store.UploadForTeam(t.Context(), store.Team(olga.team), retry.UploadID)
	if err != nil || reservation.Principal == committed.Principal {
		t.Fatalf("recovery actor = %+v, %v", reservation, err)
	}
}

func TestDirectCommitRefusesExistingObjectWithDifferentChecksum(t *testing.T) {
	client, _ := directS3(t)
	f := newAppFixture(t, func(s *controller.Server) *controller.Server {
		return s.WithDirectUploads(client, "bucket", "cache")
	})
	olga := f.ghUser(524, "olga")
	_, prefix := f.runWork(olga, "run-mismatch", "https://github.com/acme/widgets.git")
	if err := f.store.SetTokenMetered(t.Context(), prefix, true); err != nil {
		t.Fatal(err)
	}
	runner := claimedUploadGrant(t, f, olga.team, "run-mismatch", prefix)
	good := []byte("expected bytes")
	goodSum := sha256.Sum256(good)
	digest := hex.EncodeToString(goodSum[:])
	key := "artifacts/blobs/" + digest
	var upload controller.DirectUploadResponse
	if code := f.call("POST", "/api/v1/data/upload", runner, map[string]any{
		"kind": "artifact", "key": key, "size": len(good), "sha256": digest, "run_id": "run-mismatch",
	}, &upload); code != http.StatusOK {
		t.Fatalf("reserve = %d", code)
	}
	_, err := client.PutObject(t.Context(), &s3.PutObjectInput{
		Bucket: aws.String("bucket"),
		Key:    aws.String("pending/" + upload.UploadID), Body: bytes.NewReader(good),
		ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(goodSum[:])),
	})
	if err != nil {
		t.Fatal(err)
	}
	bad := []byte("forged content")
	badSum := sha256.Sum256(bad)
	_, err = client.PutObject(t.Context(), &s3.PutObjectInput{
		Bucket: aws.String("bucket"),
		Key:    aws.String("poison"), Body: bytes.NewReader(bad),
		ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(badSum[:])),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CopyObject(t.Context(), &s3.CopyObjectInput{
		Bucket:     aws.String("bucket"),
		Key:        aws.String("cache/teams/" + olga.team + "/cloud/" + key),
		CopySource: aws.String("bucket/poison"), MetadataDirective: types.MetadataDirectiveReplace,
		Metadata: map[string]string{"sha256": digest, "provenance": "cloud"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var ignored map[string]any
	if code := f.call("POST", "/api/v1/data/commit", runner, map[string]any{
		"upload_id": upload.UploadID, "run_id": "run-mismatch",
	}, &ignored); code != http.StatusConflict {
		t.Fatalf("mismatched final object = %d, want 409", code)
	}
	if _, err := f.store.CommittedObject(t.Context(), store.Team(olga.team), key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("mismatched object published: %v", err)
	}
}

func TestDirectUploadRequiresTheRunsLiveClaim(t *testing.T) {
	b := newPassBuckets(t)
	f := newAppFixture(t, func(s *controller.Server) *controller.Server {
		return s.WithDirectUploads(b.raw, passBucket, "cache")
	})
	olga := f.ghUser(501, "olga")
	rawRunner, prefix := f.runWork(olga, "run-direct", "https://github.com/acme/widgets.git")
	runner := claimedUploadGrant(t, f, olga.team, "run-direct", prefix)
	request := map[string]any{
		"kind": "binary", "key": "bin/01234567-89abcdef/" + strings.Repeat("a", 64),
		"size": 3, "sha256": strings.Repeat("a", 64), "run_id": "run-direct",
	}
	var answer controller.DirectUploadResponse
	if code := f.call("POST", "/api/v1/data/upload", runner, request, &answer); code != http.StatusOK {
		t.Fatalf("live claimant reserve = %d, want 200", code)
	}
	request["size"] = (500 << 20) + 1
	if code := f.call("POST", "/api/v1/data/upload", runner, request, &answer); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize reserve = %d, want 413", code)
	}
	request["size"] = 3
	request["key"] = "teams/victim/bin/01234567-89abcdef/" + strings.Repeat("a", 64)
	if code := f.call("POST", "/api/v1/data/upload", runner, request, &answer); code != http.StatusBadRequest {
		t.Fatalf("cross-team key = %d, want 400", code)
	}
	request["key"] = "bin/01234567-89abcdef/" + strings.Repeat("a", 64)
	tn, err := f.store.ForTeam(t.Context(), store.Team(olga.team))
	if err != nil {
		t.Fatal(err)
	}
	if err := tn.CreateRun(t.Context(), store.Run{ID: "run-second", Pipeline: "build", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateNode(t.Context(), store.Node{RunID: "run-second", NodeID: "compile", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.MarkNodeReady(t.Context(), "run-second", "compile"); err != nil {
		t.Fatal(err)
	}
	var second map[string]any
	if code := f.call("POST", "/api/v1/runs/run-second/nodes/compile/claim", rawRunner,
		map[string]any{"holder_id": "pod-second"}, &second); code != http.StatusOK {
		t.Fatalf("second claim = %d", code)
	}
	if code := f.call("POST", "/api/v1/data/commit", runner,
		map[string]any{"upload_id": answer.UploadID, "run_id": "run-second"}, &second); code != http.StatusForbidden {
		t.Fatalf("another live run committed this upload = %d, want 403", code)
	}
	if _, err := f.store.DB().ExecContext(t.Context(),
		`UPDATE nodes SET lease_expires_at = 0 WHERE run_id = ? AND node_id = ?`, "run-direct", "compile"); err != nil {
		t.Fatal(err)
	}
	if code := f.call("POST", "/api/v1/data/commit", runner,
		map[string]any{"upload_id": answer.UploadID, "run_id": "run-direct"}, &answer); code != http.StatusForbidden {
		t.Fatalf("expired claimant commit = %d, want 403", code)
	}
	request["run_id"] = "run-never-claimed"
	if code := f.call("POST", "/api/v1/data/upload", runner, request, &answer); code != http.StatusForbidden {
		t.Fatalf("stale claimant = %d, want 403", code)
	}
}

func TestRawRunnerBearerCannotSignDataWithALiveClaim(t *testing.T) {
	client, _ := directS3(t)
	f := newAppFixture(t, func(s *controller.Server) *controller.Server {
		return s.WithDirectUploads(client, "bucket", "cache")
	})
	olga := f.ghUser(525, "olga")
	rawRunner, prefix := f.runWork(olga, "run-raw", "https://github.com/acme/widgets.git")
	if err := f.store.SetTokenMetered(t.Context(), prefix, true); err != nil {
		t.Fatal(err)
	}
	grant := claimedUploadGrant(t, f, olga.team, "run-raw", prefix)
	request := map[string]any{
		"kind": "binary", "key": "bin/01234567-89abcdef/" + strings.Repeat("a", 64),
		"size": 4, "sha256": strings.Repeat("a", 64), "run_id": "run-raw",
	}
	var upload controller.DirectUploadResponse
	if code := f.call("POST", "/api/v1/data/upload", rawRunner, request, &upload); code != http.StatusForbidden {
		t.Fatalf("raw runner reserve = %d, want 403", code)
	}
	if code := f.call("POST", "/api/v1/data/upload", grant, request, &upload); code != http.StatusOK {
		t.Fatalf("claim grant reserve = %d, want 200", code)
	}
	var ignored map[string]any
	if code := f.call("POST", "/api/v1/data/commit", rawRunner, map[string]any{
		"upload_id": upload.UploadID, "run_id": "run-raw",
	}, &ignored); code != http.StatusForbidden {
		t.Fatalf("raw runner commit = %d, want 403", code)
	}
}

func TestTeamOwnerCanChooseWhetherCloudTrustsLocalBinaries(t *testing.T) {
	f := newAppFixture(t)
	olga := f.ghUser(523, "olga")
	var answer struct {
		TrustLocalBuilds bool `json:"trust_local_builds"`
	}
	if code := f.call("GET", "/api/v1/team/build-trust", olga.auth, nil, &answer); code != http.StatusOK || answer.TrustLocalBuilds {
		t.Fatalf("default trust = %d %+v", code, answer)
	}
	if code := f.call("PUT", "/api/v1/team/build-trust", olga.auth,
		map[string]bool{"trust_local_builds": true}, &answer); code != http.StatusOK || !answer.TrustLocalBuilds {
		t.Fatalf("enable trust = %d %+v", code, answer)
	}
	trust, err := f.store.TrustLocalBuilds(t.Context(), store.Team(olga.team))
	if err != nil || !trust {
		t.Fatalf("stored trust = %v, %v", trust, err)
	}
}

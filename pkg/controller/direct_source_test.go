package controller_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestSourceUploadUserBearerReservesBeforeRun(t *testing.T) {
	s3Client, objects := directS3(t)
	f := newAppFixture(t, func(s *controller.Server) *controller.Server {
		return s.WithDirectUploads(s3Client, "bucket", "cache")
	})
	user := f.ghUser(650, "source-owner")
	allowance := int64(1 << 30)
	if _, err := f.store.SetCreditSettings(t.Context(), store.CreditSettingsUpdate{StorageFreeAllowanceBytes: &allowance}); err != nil {
		t.Fatal(err)
	}
	team, err := f.store.ForTeam(t.Context(), store.Team(user.team))
	if err != nil {
		t.Fatal(err)
	}
	raw, token, err := team.CreateToken(t.Context(), "source-owner", store.TokenKindUser,
		[]string{controller.ScopeRunsWrite}, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	bundle := []byte("git bundle fixture")
	sum := sha256.Sum256(bundle)
	digest := hex.EncodeToString(sum[:])
	key := "sources/" + digest + "/" + strings.Repeat("1", 32)
	if code := f.call(http.MethodPost, "/api/v1/data/upload", "Bearer "+raw,
		map[string]any{"kind": "source", "key": "sources/" + digest, "size": len(bundle), "sha256": digest}, nil); code != http.StatusBadRequest {
		t.Fatalf("reusable digest-only source key = %d, want 400", code)
	}
	var reserved controller.DirectUploadResponse
	if code := f.call(http.MethodPost, "/api/v1/data/upload", "Bearer "+raw,
		map[string]any{"kind": "source", "key": key, "size": len(bundle), "sha256": digest},
		&reserved); code != http.StatusOK || reserved.UploadID == "" {
		t.Fatalf("pretrigger source reserve = %d, upload id %q", code, reserved.UploadID)
	}
	var runs int
	if err := f.store.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM runs WHERE team = ?`, user.team).Scan(&runs); err != nil || runs != 0 {
		t.Fatalf("source reserve created %d runs, err %v", runs, err)
	}
	if _, err := s3Client.PutObject(t.Context(), &s3.PutObjectInput{
		Bucket: aws.String("bucket"), Key: aws.String("pending/" + reserved.UploadID),
		Body: bytes.NewReader(bundle), ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(sum[:])),
	}); err != nil {
		t.Fatal(err)
	}
	if code := f.call(http.MethodPost, "/api/v1/data/commit", "Bearer "+raw,
		map[string]any{"upload_id": reserved.UploadID}, nil); code != http.StatusNoContent {
		t.Fatalf("pretrigger source commit = %d, want 204", code)
	}
	committed, err := f.store.CommittedObject(t.Context(), store.Team(user.team), key)
	if err != nil || committed.Principal != token.Principal || committed.Size != int64(len(bundle)) {
		t.Fatalf("committed source = %+v, %v", committed, err)
	}
	if got := objects()["cache/teams/"+user.team+"/local/"+key].body; !bytes.Equal(got, bundle) {
		t.Fatalf("committed S3 source bytes = %q, want %q", got, bundle)
	}
	var started struct {
		RunID string `json:"run_id"`
	}
	if code := f.call(http.MethodPost, "/api/v1/triggers", "Bearer "+raw, map[string]any{
		"pipeline": "build",
		"trigger": map[string]any{"source": "pipeline-working-tree@test", "env": map[string]string{
			"SPARKWING_SOURCE_BUNDLE_OBJECT": key,
		}},
		"git": map[string]string{"sha": strings.Repeat("a", 40)},
	}, &started); code != http.StatusAccepted || started.RunID == "" {
		t.Fatalf("source-backed trigger admission = %d, run %q", code, started.RunID)
	}
	var trigger store.Trigger
	if code := f.call(http.MethodGet, "/api/v1/triggers/"+started.RunID, user.auth, nil, &trigger); code != http.StatusOK ||
		trigger.TriggerEnv["SPARKWING_SOURCE_BUNDLE_OBJECT"] != key {
		t.Fatalf("source-backed trigger = %d, %+v", code, trigger)
	}
	cacheBucket, err := teamblob.New(teamblob.Options{Bucket: "bucket", Prefix: "cache", Client: s3Client})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.srv.WithSignedDownloads(cacheBucket, nil, s3Client, "", "", ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv(authwire.CacheGrantKeyEnv, "source-test-grant-key")
	runnerRaw, runner, err := team.CreateToken(t.Context(), "source-runner", store.TokenKindRunner,
		[]string{controller.ScopeTriggersClaim, controller.ScopeNodesClaim}, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB().ExecContext(t.Context(), `UPDATE triggers SET status = 'claimed',
		claim_principal = ?, claim_token_prefix = ?, claim_seq = 1, lease_expires_at = ? WHERE id = ?`,
		runner.Principal, runner.Prefix, time.Now().Add(time.Hour).UnixNano(), started.RunID); err != nil {
		t.Fatal(err)
	}
	grant, err := authwire.MintClaimCacheGrant(os.Getenv(authwire.CacheGrantKeyEnv), user.team, started.RunID,
		time.Now(), time.Hour, &authwire.CacheClaim{
			Kind: "trigger", Generation: 1,
			Principal: runner.Principal, TokenPrefix: runner.Prefix,
		})
	if err != nil {
		t.Fatal(err)
	}
	var download controller.DataDownloadResponse
	if code := f.call(http.MethodPost, "/api/v1/data/download", "Bearer "+grant,
		map[string]string{"kind": "source", "key": key}, &download); code != http.StatusOK ||
		!strings.Contains(download.URL, "/cache/teams/"+user.team+"/local/"+key) {
		t.Fatalf("claim source download = %d, URL %q", code, download.URL)
	}
	if code := f.call(http.MethodPost, "/api/v1/data/download", "Bearer "+grant,
		map[string]string{"kind": "artifact", "key": "artifacts/blobs/" + digest}, nil); code != http.StatusForbidden {
		t.Fatalf("pending run signed an artifact = %d, want 403", code)
	}
	if err := f.store.CreateNode(t.Context(), store.Node{RunID: started.RunID, NodeID: "compile", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	node, err := f.store.ClaimNamedNode(t.Context(), store.ClaimIdentity{Principal: runner.Principal, TokenPrefix: runner.Prefix},
		started.RunID, "compile", "source-runner", time.Hour, store.NamedClaimOptions{})
	if err != nil {
		t.Fatal(err)
	}
	nodeGrant, err := authwire.MintClaimCacheGrant(os.Getenv(authwire.CacheGrantKeyEnv), user.team, started.RunID,
		time.Now(), time.Hour, &authwire.CacheClaim{
			Kind: "node", NodeID: "compile",
			HolderID: node.ClaimedBy, MembershipID: node.ClaimMembershipID,
			ReservationID: node.ReservationID, Generation: node.ClaimGeneration,
			Principal: runner.Principal, TokenPrefix: runner.Prefix,
		})
	if err != nil {
		t.Fatal(err)
	}
	if code := f.call(http.MethodPost, "/api/v1/data/download", "Bearer "+nodeGrant,
		map[string]string{"kind": "source", "key": key}, nil); code != http.StatusForbidden {
		t.Fatalf("pending node claim signed source = %d, want 403", code)
	}
	if code := f.call(http.MethodPost, "/api/v1/triggers", "Bearer "+raw, map[string]any{
		"pipeline": "build", "trigger": map[string]any{
			"source": "pipeline-working-tree@test",
			"env":    map[string]string{"SPARKWING_SOURCE_BUNDLE_OBJECT": key},
		},
		"git": map[string]string{"sha": strings.Repeat("a", 40)},
	}, nil); code != http.StatusConflict {
		t.Fatalf("second run bound same source = %d, want 409", code)
	}
	if code := f.call(http.MethodPost, "/api/v1/data/download", "Bearer "+runnerRaw,
		map[string]string{"kind": "source", "key": key}, nil); code != http.StatusForbidden {
		t.Fatalf("raw runner bearer source download = %d, want 403", code)
	}
	if code := f.call(http.MethodPost, "/api/v1/data/download", "Bearer "+grant,
		map[string]string{"kind": "source", "key": "sources/" + strings.Repeat("b", 64)}, nil); code != http.StatusForbidden {
		t.Fatalf("claim signed another source = %d, want 403", code)
	}
	if code := f.call(http.MethodPost, "/api/v1/data/upload", "Bearer "+grant,
		map[string]any{"kind": "source", "key": key, "size": len(bundle), "sha256": digest}, nil); code == http.StatusOK {
		t.Fatal("runner grant reserved a pretrigger source")
	}
	if code := f.call(http.MethodPost, "/api/v1/data/upload", "Bearer "+raw,
		map[string]any{
			"kind": "artifact", "key": "artifacts/blobs/" + digest,
			"size": len(bundle), "sha256": digest, "run_id": started.RunID,
		}, nil); code != http.StatusForbidden {
		t.Fatalf("user bearer reserved runner artifact = %d, want 403", code)
	}
	otherRaw, _, err := team.CreateToken(t.Context(), "other-uploader", store.TokenKindUser,
		[]string{controller.ScopeRunsWrite}, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	requestTrigger := map[string]any{
		"pipeline": "build", "trigger": map[string]any{
			"source": "pipeline-working-tree@test",
			"env":    map[string]string{"SPARKWING_SOURCE_BUNDLE_OBJECT": key},
		},
		"git": map[string]string{"sha": strings.Repeat("a", 40)},
	}
	if code := f.call(http.MethodPost, "/api/v1/triggers", "Bearer "+otherRaw, requestTrigger, nil); code != http.StatusForbidden {
		t.Fatalf("another uploader in team used source = %d, want 403", code)
	}
	bob := f.ghUser(651, "other-team")
	bobTeam, err := f.store.ForTeam(t.Context(), store.Team(bob.team))
	if err != nil {
		t.Fatal(err)
	}
	bobRaw, _, err := bobTeam.CreateToken(t.Context(), "other-team", store.TokenKindUser,
		[]string{controller.ScopeRunsWrite}, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if code := f.call(http.MethodPost, "/api/v1/triggers", "Bearer "+bobRaw, requestTrigger, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("other team's source trigger = %d, want 422", code)
	}
	if _, err := f.store.DB().ExecContext(t.Context(),
		`UPDATE triggers SET lease_expires_at = ? WHERE id = ?`, time.Now().Add(-time.Minute).UnixNano(), started.RunID); err != nil {
		t.Fatal(err)
	}
	if code := f.call(http.MethodPost, "/api/v1/data/download", "Bearer "+grant,
		map[string]string{"kind": "source", "key": key}, nil); code != http.StatusForbidden {
		t.Fatalf("expired trigger claim signed its source = %d, want 403", code)
	}
	if err := team.FinishRun(t.Context(), started.RunID, "failed", "test failure"); err != nil {
		t.Fatal(err)
	}
	var retryError map[string]any
	if code := f.call(http.MethodPost, "/api/v1/runs/"+started.RunID+"/retry", user.auth, nil, &retryError); code != http.StatusUnprocessableEntity ||
		!strings.Contains(fmt.Sprint(retryError["error"]), "new source upload") {
		t.Fatalf("retry reused one-run source = %d, %+v, want 422 with reupload guidance", code, retryError)
	}
}

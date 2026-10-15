package controller

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type logHTTPObjects struct {
	blob.ObjectBackend
	signed []string
	reads  int
	bytes  map[string][]byte
}

func (o *logHTTPObjects) SignLogPut(_ context.Context, key string, _ time.Time) (string, map[string]string, error) {
	o.signed = append(o.signed, key)
	return "https://objects.invalid/" + key, map[string]string{"If-None-Match": "*"}, nil
}
func (o *logHTTPObjects) Get(_ context.Context, key string) (io.ReadCloser, error) {
	o.reads++
	body, ok := o.bytes[key]
	if !ok {
		return nil, blob.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

func TestLogSubmissionRoutesCommitOnlyTheirExactAuthorizedSubject(t *testing.T) {
	e, _, raw := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
	objects := &logHTTPObjects{bytes: map[string][]byte{}}
	server := New(e.Store, nil).WithEngineController(e).WithDataController(&DataController{Store: e.Store, Objects: objects})
	h := server.Handler()
	response := engineClaimHTTP(t, h, raw, "POST", "/api/v1/runners/claim", `{"runner_version":"v1","kinds":["node"],"available_cores":1}`, 200)
	var award controllerwire.ClaimResponse
	if err := json.Unmarshal(response.Body.Bytes(), &award); err != nil || award.Claim == nil {
		t.Fatalf("claim: %v", err)
	}
	claim := award.Claim
	engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/transition", `{"to":"running","cause":"start","source_attempt":0}`, 200)
	subject := blob.LogSubject{Kind: blob.LogJobAttempt, Team: "a", RunID: "run", Attempt: &runspec.AttemptIdentity{Team: "a", RunID: "run", NodeID: "job", Attempt: 1, Generation: claim.Binding.Generation}}
	signing := blob.LogSigningRequest{Subject: subject, FirstSegment: 1, Count: 2}
	encoded, err := json.Marshal(signing)
	if err != nil {
		t.Fatal(err)
	}
	response = engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/log-segments", string(encoded), 200)
	var batch blob.LogUploadBatch
	if err := json.Unmarshal(response.Body.Bytes(), &batch); err != nil || batch.RetentionClass != "r30" || len(objects.signed) != 3 {
		t.Fatalf("batch=%+v: %v", batch, err)
	}
	key, err := blob.LogObjectKey(subject, batch.RetentionClass, 1)
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("masked log\n")
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	if _, err := z.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	objects.bytes[key] = compressed.Bytes()
	digest := sha256.Sum256(compressed.Bytes())
	first := store.Segment{Index: 1, Size: int64(len(plain)), StoredBytes: int64(compressed.Len()), Digest: hex.EncodeToString(digest[:])}
	second := store.Segment{Index: 2, Offset: int64(len(plain)), Size: int64(len(plain)), StoredBytes: int64(compressed.Len()), Digest: first.Digest}
	secondKey, err := blob.LogObjectKey(subject, batch.RetentionClass, 2)
	if err != nil {
		t.Fatal(err)
	}
	objects.bytes[secondKey] = compressed.Bytes()
	for _, segment := range []store.Segment{first, second} {
		encoded, err := json.Marshal(logCommitRequest{Subject: subject, Segments: []store.Segment{segment}})
		if err != nil {
			t.Fatal(err)
		}
		engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/log-manifest", string(encoded), 200)
	}
	commit := logCommitRequest{Subject: subject, Segments: []store.Segment{first, second}, Final: true}
	encoded, err = json.Marshal(commit)
	if err != nil {
		t.Fatal(err)
	}
	engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/log-manifest", string(encoded), 200)
	engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/log-manifest", string(encoded), 200)
	tenant, err := e.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := e.Store.LookupCapability(t.Context(), sha256.Sum256([]byte(claim.Capability)))
	if err != nil {
		t.Fatal(err)
	}
	a := capability.Authorized{Principal: principal, Binding: claim.Binding, Target: capability.Target{Team: "a", RunID: "run", NodeID: "job"}}
	view, err := tenant.ReadLogSegments(t.Context(), engineActor(a), blob.LogReadRequest{Subject: subject})
	if err != nil || !view.Complete || len(view.Segments) != 2 {
		t.Fatalf("view=%+v: %v", view, err)
	}
	history := engineClaimHTTP(t, h, claim.Capability, "GET", "/api/v1/runs/run/jobs/job/attempts", "", 200)
	if strings.Contains(history.Body.String(), "holder") || strings.Contains(history.Body.String(), "capability") || !strings.Contains(history.Body.String(), `"attempt":1`) {
		t.Fatalf("private or missing attempt projection: %s", history.Body.String())
	}
	initial := len(objects.signed)
	property := func(choice uint8, noise string) bool {
		wrong := signing
		identity := *signing.Subject.Attempt
		wrong.Subject.Attempt = &identity
		switch choice % 4 {
		case 0:
			wrong.Subject.Team += noise + "x"
			identity.Team = wrong.Subject.Team
		case 1:
			wrong.Subject.RunID += noise + "x"
			identity.RunID = wrong.Subject.RunID
		case 2:
			identity.NodeID += noise + "x"
		case 3:
			identity.Generation++
		}
		encoded, err := json.Marshal(wrong)
		if err != nil {
			return false
		}
		response := engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/log-segments", string(encoded), http.StatusBadRequest)
		return !strings.Contains(response.Body.String(), "https://objects") && len(objects.signed) == initial
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 30}); err != nil {
		t.Fatal(err)
	}
}

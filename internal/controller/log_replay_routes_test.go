package controller

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// A runner that loses the PUT or commit response replays the same slot through
// the authenticated routes: storage and the retained-byte charge stay single.
func TestLogSubmissionRoutesReplayLostPutAndCommit(t *testing.T) {
	e, _, raw := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{
		Directory: t.TempDir(), Endpoint: "https://controller.invalid", SigningKey: bytes.Repeat([]byte{43}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	h := New(e.Store, nil).WithEngineController(e.EngineController).WithDataController(NewDataController(e.Store, DataController{Objects: objects})).Handler()
	response := engineClaimHTTP(t, h, raw, "POST", "/api/v1/runners/claim", `{"runner_version":"v1","kinds":["node"]}`, 200)
	var award controllerwire.ClaimResponse
	if err := json.Unmarshal(response.Body.Bytes(), &award); err != nil || award.Claim == nil {
		t.Fatalf("claim: %v", err)
	}
	claim := award.Claim
	engineAdmitBearer(t, e.Store, claim.Capability)
	engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/transition", `{"to":"running","cause":"start","source_attempt":0}`, 200)
	subject := blob.LogSubject{Kind: blob.LogJobAttempt, Team: "a", RunID: "run", Attempt: &runspec.AttemptIdentity{Team: "a", RunID: "run", NodeID: "job", Attempt: 1, Generation: claim.Binding.Generation}}
	plain := []byte("replayed log\n")
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	if _, err := z.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(compressed.Bytes())
	segment := runstate.Segment{Index: 1, Size: int64(len(plain)), StoredBytes: int64(compressed.Len()), Digest: hex.EncodeToString(sum[:])}
	sign := func() blob.LogUploadURL {
		t.Helper()
		encoded, err := json.Marshal(blob.LogSigningRequest{Subject: subject, Index: 1, Size: segment.StoredBytes, Digest: blob.Digest(segment.Digest)})
		if err != nil {
			t.Fatal(err)
		}
		var slot blob.LogUploadURL
		if err := json.Unmarshal(engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/log-segments", string(encoded), http.StatusOK).Body.Bytes(), &slot); err != nil {
			t.Fatal(err)
		}
		return slot
	}
	put := func(slot blob.LogUploadURL) int {
		t.Helper()
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPut, slot.URL, bytes.NewReader(compressed.Bytes()))
		for key, value := range slot.Headers {
			request.Header.Set(key, value)
		}
		h.ServeHTTP(response, request)
		return response.Code
	}
	commit := func() {
		t.Helper()
		encoded, err := json.Marshal(logCommitRequest{Subject: subject, Segments: []runstate.Segment{segment}})
		if err != nil {
			t.Fatal(err)
		}
		engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/log-manifest", string(encoded), http.StatusOK)
	}
	tenant, err := e.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	retained := func() int64 {
		t.Helper()
		n, err := tenant.StorageRetainedBytes(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := retained()
	first := sign()
	charged := retained()
	if first.Committed || first.URL == "" || charged != before+segment.StoredBytes {
		t.Fatalf("first signing committed=%v url=%v, retained %d -> %d; want one uncommitted URL charging %d bytes", first.Committed, first.URL != "", before, charged, segment.StoredBytes)
	}
	if code := put(first); code != http.StatusCreated {
		t.Fatalf("first PUT = %d", code)
	}
	// Lost PUT response: the runner cannot tell whether its bytes landed.
	again := sign()
	if again.Committed || again.URL == "" || !again.ExpiresAt.Equal(first.ExpiresAt) || retained() != charged {
		t.Fatalf("re-signed lost PUT committed=%v url=%v expires=%v, retained %d; want the same uncommitted slot until %v and charge %d", again.Committed, again.URL != "", again.ExpiresAt, retained(), first.ExpiresAt, charged)
	}
	if code := put(again); code != http.StatusCreated {
		t.Fatalf("replayed identical PUT = %d", code)
	}
	commit()
	// Lost commit response: re-signing reports the slot committed and grants no URL.
	if replay := sign(); !replay.Committed || replay.URL != "" || len(replay.Headers) != 0 {
		t.Fatalf("re-signed committed slot committed=%v url=%v headers=%d; want committed with no upload URL", replay.Committed, replay.URL != "", len(replay.Headers))
	}
	commit()
	if after := retained(); after != charged {
		t.Fatalf("replays changed retained storage from %d to %d", charged, after)
	}
	principal, err := e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(claim.Capability)))
	if err != nil {
		t.Fatal(err)
	}
	a := capability.Authorized{Principal: principal, Binding: claim.Binding, Target: capability.Target{Team: "a", RunID: "run", NodeID: "job"}}
	view, err := tenant.ReadLogSegments(t.Context(), engineActor(a), blob.LogReadRequest{Subject: subject})
	if err != nil || len(view.Segments) != 1 || view.Segments[0].Index != 1 || view.Segments[0].Digest != segment.Digest {
		t.Fatalf("committed view = %+v, %v; want segment 1 recorded once", view, err)
	}
}

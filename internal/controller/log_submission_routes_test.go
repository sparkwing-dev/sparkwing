package controller

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestLogSubmissionRoutesCommitOnlyTheirExactAuthorizedSubject(t *testing.T) {
	e, _, raw := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{
		Directory: t.TempDir(), Endpoint: "https://controller.invalid", SigningKey: bytes.Repeat([]byte{41}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	server := New(e.Store, nil).WithEngineController(e.EngineController).WithDataController(NewDataController(e.Store, DataController{Objects: objects}))
	h := server.Handler()
	response := engineClaimHTTP(t, h, raw, "POST", "/api/v1/runners/claim", `{"runner_version":"v1","kinds":["node"],"available_cores":1}`, 200)
	var award controllerwire.ClaimResponse
	if err := json.Unmarshal(response.Body.Bytes(), &award); err != nil || award.Claim == nil {
		t.Fatalf("claim: %v", err)
	}
	claim := award.Claim
	engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/transition", `{"to":"running","cause":"start","source_attempt":0}`, 200)
	subject := blob.LogSubject{Kind: blob.LogJobAttempt, Team: "a", RunID: "run", Attempt: &runspec.AttemptIdentity{Team: "a", RunID: "run", NodeID: "job", Attempt: 1, Generation: claim.Binding.Generation}}
	plain := []byte("masked log\n")
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	if _, err := z.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(compressed.Bytes())
	first := store.Segment{Index: 1, Size: int64(len(plain)), StoredBytes: int64(compressed.Len()), Digest: hex.EncodeToString(digest[:])}
	second := store.Segment{Index: 2, Offset: int64(len(plain)), Size: int64(len(plain)), StoredBytes: int64(compressed.Len()), Digest: first.Digest}
	sign := func(index, size int64, status int) blob.LogUploadURL {
		encoded, err := json.Marshal(blob.LogSigningRequest{Subject: subject, Index: index, Size: size, Digest: blob.Digest(first.Digest)})
		if err != nil {
			t.Fatal(err)
		}
		response := engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/log-segments", string(encoded), status)
		var slot blob.LogUploadURL
		if status == 200 {
			if err := json.Unmarshal(response.Body.Bytes(), &slot); err != nil || slot.RetentionClass != "r30" || slot.Index != index || slot.URL == "" {
				t.Fatalf("slot=%+v: %v", slot, err)
			}
		}
		return slot
	}
	sign(1, 0, 400)
	sign(1, limits.LogSegmentUploadBytes+1, 400)
	sign(0, limits.LogManifestBytes+1, 400)
	put := func(slot blob.LogUploadURL, body []byte) int {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPut, slot.URL, bytes.NewReader(body))
		for key, value := range slot.Headers {
			request.Header.Set(key, value)
		}
		h.ServeHTTP(response, request)
		return response.Code
	}
	for _, index := range []int64{1, 2} {
		slot := sign(index, int64(compressed.Len()), 200)
		if code := put(slot, append(bytes.Clone(compressed.Bytes()), 0)); code != http.StatusBadRequest {
			t.Fatalf("log PUT with unsigned length = %d", code)
		}
		if code := put(slot, compressed.Bytes()); code != http.StatusCreated {
			t.Fatalf("log PUT = %d", code)
		}
	}
	for _, segment := range []store.Segment{first, second} {
		encoded, err := json.Marshal(logCommitRequest{Subject: subject, Segments: []store.Segment{segment}})
		if err != nil {
			t.Fatal(err)
		}
		engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/log-manifest", string(encoded), 200)
	}
	commit := logCommitRequest{Subject: subject, Segments: []store.Segment{first, second}, Final: true}
	encoded, err := json.Marshal(commit)
	if err != nil {
		t.Fatal(err)
	}
	engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/log-manifest", string(encoded), 200)
	engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/log-manifest", string(encoded), 200)
	tenant, err := e.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(claim.Capability)))
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
	var output bytes.Buffer
	read, err := (logseg.CommittedReader{Records: tenant, Objects: objects}).Read(t.Context(), engineActor(a), blob.LogReadRequest{Subject: subject}, &output)
	if err != nil || !read.Complete || !bytes.Equal(output.Bytes(), bytes.Repeat(plain, 2)) {
		t.Fatalf("committed bytes = %q, %+v: %v", output.Bytes(), read, err)
	}
	response = engineClaimHTTP(t, h, claim.Capability, "GET", "/api/v1/runs/run/jobs/job/logs", "", http.StatusOK)
	if !bytes.Equal(response.Body.Bytes(), output.Bytes()) {
		t.Fatalf("controller log read = %q; want %q", response.Body.Bytes(), output.Bytes())
	}
	signing := blob.LogSigningRequest{Subject: subject, Index: 1, Size: first.StoredBytes, Digest: blob.Digest(first.Digest)}
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
		return !strings.Contains(response.Body.String(), "https://controller.invalid")
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 30}); err != nil {
		t.Fatal(err)
	}
}

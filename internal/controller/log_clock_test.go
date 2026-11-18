package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestLogSlotSigningSurvivesBackwardWallClock(t *testing.T) {
	for _, tc := range []struct {
		name string
		skew time.Duration
	}{

		{name: "backward_1ms", skew: -time.Millisecond},
		{name: "backward_3300ms", skew: -3300 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slotNow := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
			e, _, raw := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim}, teststore.WithClock(func() time.Time { return slotNow }))
			signerNow := slotNow.Add(tc.skew)
			options := blob.FilesystemOptions{
				Directory: t.TempDir(), Endpoint: "http://local", SigningKey: bytes.Repeat([]byte{42}, 32),
				Now: func() time.Time { return signerNow }, LogNow: e.Store.LogUploadNow,
			}
			objects, err := blob.NewFilesystem(options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = objects.Close() })
			h := e.Server.WithDataObjects(objects).Handler()
			response := engineClaimHTTP(t, h, raw, "POST", "/api/v1/runners/claim", `{"runner_version":"v1","kinds":["node"]}`, http.StatusOK)
			var award controllerwire.ClaimResponse
			if err := json.Unmarshal(response.Body.Bytes(), &award); err != nil || award.Claim == nil {
				t.Fatalf("claim: %v", err)
			}
			claim := award.Claim
			engineAdmitBearer(t, e.Store, claim.Capability)
			engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/transition", `{"to":"running","cause":"start","source_attempt":0}`, http.StatusOK)
			subject := blob.LogSubject{Kind: blob.LogJobAttempt, Team: "a", RunID: "run", Attempt: &runspec.AttemptIdentity{Team: "a", RunID: "run", NodeID: "job", Attempt: 1, Generation: claim.Binding.Generation}}
			body, err := json.Marshal(blob.LogSigningRequest{Subject: subject, Index: 1, Size: 1, Digest: blob.Digest(fmt.Sprintf("%x", sha256.Sum256([]byte("x"))))})
			if err != nil {
				t.Fatal(err)
			}
			signed := engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/log-segments", string(body), http.StatusOK)
			var upload blob.LogUploadURL
			if err := json.Unmarshal(signed.Body.Bytes(), &upload); err != nil {
				t.Fatal(err)
			}
			if !upload.ExpiresAt.Equal(slotNow.Add(15 * time.Minute)) {
				t.Fatalf("expiry = %s", upload.ExpiresAt)
			}
			request := httptest.NewRequest(http.MethodPut, upload.URL, strings.NewReader("x"))
			for key, value := range upload.Headers {
				request.Header.Set(key, value)
			}
			responsePut := httptest.NewRecorder()
			objects.ServeHTTP(responsePut, request)
			if responsePut.Code != http.StatusCreated {
				t.Fatalf("PUT = %d: %s", responsePut.Code, responsePut.Body)
			}
		})
	}
}

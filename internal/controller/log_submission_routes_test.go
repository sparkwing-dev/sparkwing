package controller

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestCompletedTriggerLogRouteSealsQuietStream(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: t.TempDir(), Endpoint: "http://local", SigningKey: bytes.Repeat([]byte{41}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	engineHTTPRoot(t, e, "a", "runner", []capability.Grant{capability.TriggersClaim})
	claim, err := e.Store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "runner", Target: &store.UnitRef{Kind: capability.UnitTrigger, RunID: "run"}})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256(claim.Bearer))
	if err != nil {
		t.Fatal(err)
	}
	owner := capability.Authorized{Principal: principal, Binding: principal.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run"}}
	if _, err := e.Finish(t.Context(), owner, controllerwire.FinishRequest{}); err != nil {
		t.Fatal(err)
	}
	subject := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "a", RunID: "run", Unit: &blob.LogUnit{Kind: blob.LogTrigger, Generation: int64(claim.Award.Generation)}}
	body, err := json.Marshal(logCommitRequest{Subject: subject, Final: true})
	if err != nil {
		t.Fatal(err)
	}
	h := e.Server.Handler()
	engineClaimHTTP(t, h, string(claim.Bearer), "POST", "/api/v1/claims/"+url.PathEscape(principal.Capability.UnitID)+"/log-manifest", string(body), http.StatusOK)
	options, _, _ := memberObservationOptions(t, e.Store, objects)
	reader, err := client.NewLogReader(options)
	if err != nil {
		t.Fatal(err)
	}
	page, err := reader.ReadLogs(t.Context(), "run", controllerwire.LogQuery{})
	if err != nil || !page.Complete {
		t.Fatalf("quiet log follow did not complete: %+v %v", page, err)
	}
}

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
	response := engineClaimHTTP(t, h, raw, "POST", "/api/v1/runners/claim", `{"runner_version":"v1","kinds":["node"]}`, 200)
	var award controllerwire.ClaimResponse
	if err := json.Unmarshal(response.Body.Bytes(), &award); err != nil || award.Claim == nil {
		t.Fatalf("claim: %v", err)
	}
	claim := award.Claim
	engineAdmitBearer(t, e.Store, claim.Capability)
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
	first := runstate.Segment{Index: 1, Size: int64(len(plain)), StoredBytes: int64(compressed.Len()), Digest: hex.EncodeToString(digest[:])}
	second := runstate.Segment{Index: 2, Offset: int64(len(plain)), Size: int64(len(plain)), StoredBytes: int64(compressed.Len()), Digest: first.Digest}
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
	for _, segment := range []runstate.Segment{first, second} {
		encoded, err := json.Marshal(logCommitRequest{Subject: subject, Segments: []runstate.Segment{segment}})
		if err != nil {
			t.Fatal(err)
		}
		engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/log-manifest", string(encoded), 200)
	}
	// The final commit verifies the stored manifest, since a create-only refusal on its PUT proves nothing about it.
	manifest := []byte(`{"status":"complete"}`)
	manifestSum := sha256.Sum256(manifest)
	signManifest, err := json.Marshal(blob.LogSigningRequest{Subject: subject, Index: 0, Size: int64(len(manifest)), Digest: blob.Digest(hex.EncodeToString(manifestSum[:]))})
	if err != nil {
		t.Fatal(err)
	}
	var manifestSlot blob.LogUploadURL
	if err := json.Unmarshal(engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/log-segments", string(signManifest), 200).Body.Bytes(), &manifestSlot); err != nil {
		t.Fatal(err)
	}
	if code := put(manifestSlot, manifest); code != http.StatusCreated {
		t.Fatalf("manifest PUT = %d", code)
	}
	otherSum := sha256.Sum256([]byte("other"))
	commit := logCommitRequest{Subject: subject, Segments: []runstate.Segment{first, second}, Final: true,
		Manifest: &blob.LogUploadCheck{Subject: subject, Size: int64(len(manifest)), Digest: blob.Digest(hex.EncodeToString(otherSum[:]))}}
	encoded, err := json.Marshal(commit)
	if err != nil {
		t.Fatal(err)
	}
	engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/log-manifest", string(encoded), http.StatusUnprocessableEntity)
	commit.Manifest.Digest = blob.Digest(hex.EncodeToString(manifestSum[:]))
	if encoded, err = json.Marshal(commit); err != nil {
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

func TestAttemptLogsWithoutSegmentsDoNotNeedPhysicalBackend(t *testing.T) {
	e, _, raw := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
	server := New(e.Store, nil).WithEngineController(e.EngineController)
	h := server.Handler()
	response := engineClaimHTTP(t, h, raw, "POST", "/api/v1/runners/claim", `{"runner_version":"v1","kinds":["node"]}`, 200)
	var award controllerwire.ClaimResponse
	if err := json.Unmarshal(response.Body.Bytes(), &award); err != nil || award.Claim == nil {
		t.Fatalf("claim: %v", err)
	}
	claim := award.Claim
	engineAdmitBearer(t, e.Store, claim.Capability)
	engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/transition", `{"to":"running","cause":"start","source_attempt":0}`, 200)
	subject := blob.LogSubject{Kind: blob.LogJobAttempt, Team: "a", RunID: "run", Attempt: &runspec.AttemptIdentity{Team: "a", RunID: "run", NodeID: "job", Attempt: 1, Generation: claim.Binding.Generation}}
	view, err := teststore.ForTeam(t, e.Store, "a").ReadRunView(t.Context(), "run")
	if err != nil || !slices.ContainsFunc(view.Logs, func(got blob.LogSubject) bool { return reflect.DeepEqual(got, subject) }) {
		t.Fatalf("attempt start did not register its stream: %+v %v", view.Logs, err)
	}
	body, _ := json.Marshal(logCommitRequest{Subject: subject, Final: true})
	engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/log-manifest", string(body), 200)
}

// A log slot's bytes pass the storage rule before the controller signs anything, so a team at its storage limit
// gets a quota refusal and no URL to upload to.
func TestLogSigningRefusesATeamAtItsStorageLimit(t *testing.T) {
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
	sign := func(index, size int64, status int) *httptest.ResponseRecorder {
		encoded, err := json.Marshal(blob.LogSigningRequest{Subject: subject, Index: index, Size: size, Digest: blob.Digest(strings.Repeat("a", 64))})
		if err != nil {
			t.Fatal(err)
		}
		return engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/log-segments", string(encoded), status)
	}
	firstURL := sign(1, 3, http.StatusOK)
	tenant, err := e.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	before, err := tenant.StorageRetainedBytes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// A signed slot keeps its first size: asking again for more bytes stores no more.
	againURL := sign(1, 4, http.StatusOK)
	if after, err := tenant.StorageRetainedBytes(t.Context()); err != nil || after != before {
		t.Fatalf("re-signing a 3-byte slot for 4 bytes changed retained storage from %d to %d: %v", before, after, err)
	}
	// The URL's deadline is fixed at first signing; once it passes, no new URL exists for the uncommitted slot.
	var first, again blob.LogUploadURL
	if json.Unmarshal(firstURL.Body.Bytes(), &first) != nil || json.Unmarshal(againURL.Body.Bytes(), &again) != nil || first.ExpiresAt.IsZero() || !again.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatalf("re-signed deadline %v; want the first %v", again.ExpiresAt, first.ExpiresAt)
	}
	if _, err := teststore.SQL(t, e.Store).ExecContext(t.Context(), `UPDATE log_slots SET created_at=created_at-? WHERE team='a' AND n=1`, int64(limits.LogUploadURLTTL)); err != nil {
		t.Fatal(err)
	}
	sign(1, 3, http.StatusConflict)
	if err := tenant.SetStorageQuota(t.Context(), store.StorageQuota{MaxBytesPerRun: 4}); err != nil {
		t.Fatal(err)
	}
	refused := sign(2, 1<<20, http.StatusTooManyRequests)
	if strings.Contains(refused.Body.String(), "url") || !strings.Contains(refused.Body.String(), "contact support to raise this limit") {
		t.Fatalf("refused signing = %s; want a quota refusal naming the remedy and no URL", refused.Body.String())
	}
}

type lifecycleProbeObjects struct {
	*blob.Filesystem
	reads *int
}

func (o lifecycleProbeObjects) LogClassReady(context.Context, string) error {
	*o.reads++
	return errors.New("bucket has no lifecycle rule")
}

func TestLogSigningNeverReadsTheBucketLifecycle(t *testing.T) {
	e, _, raw := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
	fsObjects, err := blob.NewFilesystem(blob.FilesystemOptions{
		Directory: t.TempDir(), Endpoint: "https://controller.invalid", SigningKey: bytes.Repeat([]byte{41}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fsObjects.Close(); err != nil {
			t.Error(err)
		}
	})
	reads := 0
	objects := lifecycleProbeObjects{Filesystem: fsObjects, reads: &reads}
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
	for index := int64(1); index <= 3; index++ {
		encoded, err := json.Marshal(blob.LogSigningRequest{Subject: subject, Index: index, Size: 3, Digest: blob.Digest(strings.Repeat("a", 64))})
		if err != nil {
			t.Fatal(err)
		}
		engineClaimHTTP(t, h, claim.Capability, "POST", "/api/v1/runs/run/jobs/job/log-segments", string(encoded), http.StatusOK)
	}
	if reads != 0 {
		t.Fatalf("log signing read the bucket lifecycle %d times; a backstop rule is checked at startup only", reads)
	}
}

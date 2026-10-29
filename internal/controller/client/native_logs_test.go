package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/storage/logseg"
)

func TestNativeLogCommitRequiresAcknowledgement(t *testing.T) {
	for _, acknowledged := range []bool{false, true} {
		t.Run(map[bool]string{false: "refused", true: "recorded"}[acknowledged], func(t *testing.T) {
			subject := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "team", RunID: "run", Unit: &blob.LogUnit{Kind: "plan", Generation: 1}}
			called := false
			transport := testEngine(t, func(request *http.Request) (*http.Response, error) {
				called = true
				if request.Method != http.MethodPost || request.URL.Path != "/api/v1/claims/plan/log-manifest" {
					t.Fatalf("unexpected commit route: %s %s", request.Method, request.URL.Path)
				}
				var body struct {
					Subject  blob.LogSubject `json:"subject"`
					Segments []any           `json:"segments"`
					Final    bool            `json:"final"`
					Reason   string          `json:"incomplete_reason"`
				}
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.Subject.RunID != subject.RunID || len(body.Segments) != 0 || !body.Final || body.Reason != "" {
					t.Fatalf("invalid log commit: %+v", body)
				}
				if acknowledged {
					return engineResponse(200, `{"recorded":true}`), nil
				}
				return engineResponse(200, `{"recorded":false}`), nil
			})
			uploader := &nativeLogUploader{transport: transport, subject: subject, pattern: "/api/v1/claims/{claim}", path: "/api/v1/claims/plan"}
			err := uploader.commit(t.Context(), nil, &blob.LogUploadCheck{Subject: subject}, "")
			if !called || (err == nil) != acknowledged {
				t.Fatalf("commit acknowledgement lost: called=%v error=%v", called, err)
			}
		})
	}
}

func TestNativeLogUploaderUsesCommittedBearerWithoutMetadataReads(t *testing.T) {
	for _, kind := range []string{"plan", "eval", "node", "adopted-node"} {
		t.Run(kind, func(t *testing.T) {
			raw := "private-capability"
			if kind == "adopted-node" {
				raw = "independent-node-award"
			}
			p := capability.Principal{Capability: capability.Capability{Prefix: "award", Digest: sha256.Sum256([]byte(raw)), Team: "team", Unit: capability.UnitPlan, UnitID: "unit", Binding: capability.Binding{RunID: "run", Generation: 2}}}
			subject := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "team", RunID: "run", Unit: &blob.LogUnit{Kind: "plan", Generation: 2}}
			path := "/api/v1/claims/unit/log-manifest"
			if kind == "eval" {
				p.Capability.Unit = capability.UnitEval
				subject.Unit.Kind = "eval"
				subject.Unit.Source = &blob.LogSource{NodeID: "source", Generation: 1}
			}
			if kind == "node" || kind == "adopted-node" {
				p.Capability.Unit = capability.UnitNode
				p.Capability.Binding.NodeID, p.Capability.Binding.Attempt = "job", 3
				subject.Kind, subject.Unit = blob.LogJobAttempt, nil
				subject.Attempt = &runspec.AttemptIdentity{Team: "team", RunID: "run", NodeID: "job", Generation: 2, Attempt: 3}
				path = "/api/v1/runs/run/jobs/job/log-manifest"
			}
			calls := 0
			transport := testEngine(t, func(request *http.Request) (*http.Response, error) {
				calls++
				if request.Method != http.MethodPost || request.URL.Path != path || request.Header.Get("Authorization") != "Bearer "+raw {
					t.Errorf("log upload performed an unrelated metadata read or lost authority: %s %s", request.Method, request.URL.Path)
					return engineResponse(503, `{}`), nil
				}
				return engineResponse(200, `{"recorded":true}`), nil
			})
			host := &LocalHostClient{run: "run", transport: transport}
			if kind == "node" {
				host.node = "job"
			}
			if kind == "adopted-node" {
				host.nodes = map[string]localNodeBearer{"job": {raw: raw, prefix: "award", generation: 2}}
			}
			uploader, err := host.LogUploader(t.Context(), p, subject)
			if err != nil || calls != 0 {
				t.Fatalf("opening an uploader contacted the controller: calls=%d error=%v", calls, err)
			}
			if err := uploader.(*nativeLogUploader).commit(t.Context(), nil, &blob.LogUploadCheck{Subject: subject}, "tail lost"); err != nil || calls != 1 {
				t.Fatalf("log commit lost its bound bearer: calls=%d error=%v", calls, err)
			}
			p.Capability.Digest = sha256.Sum256([]byte("another-award"))
			if _, err := host.LogUploader(context.Background(), p, subject); err == nil || calls != 1 {
				t.Fatal("changed bearer reached log persistence")
			}
		})
	}
}

// A segment whose commit succeeded but whose acknowledgement was lost recovers on the retry: signing reports the
// slot committed, and the uploader records the segment without uploading or committing it again.
func TestNativeLogUploaderRecoversALostCommitAcknowledgement(t *testing.T) {
	subject := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "team", RunID: "run", Unit: &blob.LogUnit{Kind: "plan", Generation: 1}}
	signs := 0
	transport := testEngine(t, func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/v1/claims/plan/log-segments" {
			t.Fatalf("a committed slot was uploaded or committed again: %s %s", request.Method, request.URL.Path)
		}
		signs++
		body, err := json.Marshal(blob.LogUploadURL{Subject: subject, RetentionClass: "r30", Index: 1, Committed: true})
		if err != nil {
			t.Fatal(err)
		}
		return engineResponse(200, string(body)), nil
	})
	uploader := &nativeLogUploader{transport: transport, subject: subject, pattern: "/api/v1/claims/{claim}", path: "/api/v1/claims/plan"}
	segment := logseg.Segment{N: 1, Bytes: 3, StoredBytes: 3, SHA256: strings.Repeat("a", 64)}
	for range 2 {
		if err := uploader.PutSegment(t.Context(), segment, []byte("abc")); err != nil {
			t.Fatal(err)
		}
	}
	if signs != 2 || len(uploader.segments) != 1 || uploader.segments[0].Index != 1 {
		t.Fatalf("signs=%d segments=%+v; want the committed segment recorded once", signs, uploader.segments)
	}
}

// fakeLogStore is a create-only object store and a commit route that verifies stored bytes, each able to lose one
// response after it has acted.
type fakeLogStore struct {
	t                      *testing.T
	subject                blob.LogSubject
	objects                map[string][]byte
	committed              map[int64]bool
	sealed                 bool
	losePut, loseCommit    map[string]bool
	puts, refused, commits int
}

func (f *fakeLogStore) handle(request *http.Request) (*http.Response, error) {
	switch {
	case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/log-segments"):
		var body blob.LogSigningRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			f.t.Fatal(err)
		}
		key := strconv.FormatInt(body.Index, 10)
		slot := blob.LogUploadURL{Subject: f.subject, RetentionClass: "r30", Index: body.Index, Committed: f.committed[body.Index]}
		if !slot.Committed {
			slot.URL, slot.ExpiresAt = "http://objects/"+key, time.Now().Add(time.Minute)
		}
		raw, _ := json.Marshal(slot)
		return engineResponse(200, string(raw)), nil
	case request.Method == http.MethodPut:
		key := strings.TrimPrefix(request.URL.Path, "/")
		if _, ok := f.objects[key]; ok {
			f.refused++
			return engineResponse(http.StatusPreconditionFailed, ""), nil
		}
		raw, err := io.ReadAll(request.Body)
		if err != nil {
			f.t.Fatal(err)
		}
		f.puts++
		f.objects[key] = raw
		if f.losePut[key] {
			delete(f.losePut, key)
			return nil, errors.New("connection reset after the object was stored")
		}
		return engineResponse(200, ""), nil
	case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/log-manifest"):
		var body struct {
			Segments []runstate.Segment   `json:"segments"`
			Final    bool                 `json:"final"`
			Manifest *blob.LogUploadCheck `json:"manifest"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			f.t.Fatal(err)
		}
		verify := func(index, size int64, digest string) bool {
			stored, ok := f.objects[strconv.FormatInt(index, 10)]
			sum := sha256.Sum256(stored)
			return ok && int64(len(stored)) == size && hex.EncodeToString(sum[:]) == digest
		}
		key := "commit"
		for _, segment := range body.Segments {
			if !verify(segment.Index, segment.StoredBytes, segment.Digest) {
				return engineResponse(409, `{"code":"integrity_failure","message":"stored segment differs"}`), nil
			}
			key = "commit-" + strconv.FormatInt(segment.Index, 10)
		}
		if body.Final {
			if body.Manifest == nil || !verify(0, body.Manifest.Size, string(body.Manifest.Digest)) {
				return engineResponse(409, `{"code":"integrity_failure","message":"stored manifest differs"}`), nil
			}
			key, f.sealed = "commit-0", true
			f.committed[0] = true
		}
		for _, segment := range body.Segments {
			f.committed[segment.Index] = true
		}
		f.commits++
		if f.loseCommit[key] {
			delete(f.loseCommit, key)
			return nil, errors.New("connection reset after the commit was recorded")
		}
		return engineResponse(200, `{"recorded":true}`), nil
	}
	f.t.Fatalf("unexpected request %s %s", request.Method, request.URL)
	return nil, nil
}

func logSegment(n int, body []byte) logseg.Segment {
	sum := sha256.Sum256(body)
	return logseg.Segment{N: n, Bytes: int64(len(body)), StoredBytes: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}
}

// A PUT or commit whose response is lost is retried: a create-only refusal (412) moves on to a commit that verifies
// the stored bytes, a lost acknowledgement finds its slot committed, and later segments and the manifest proceed.
// Every failed attempt reports an error, so the sink keeps the bytes in its spool until the commit succeeds.
func TestNativeLogUploaderRecoversLostUploadAndCommitResponses(t *testing.T) {
	subject := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "team", RunID: "run", Unit: &blob.LogUnit{Kind: "plan", Generation: 1}}
	store := &fakeLogStore{
		t: t, subject: subject, objects: map[string][]byte{}, committed: map[int64]bool{},
		losePut: map[string]bool{"1": true, "0": true}, loseCommit: map[string]bool{"commit-2": true},
	}
	uploader := &nativeLogUploader{transport: testEngine(t, store.handle), subject: subject, pattern: "/api/v1/claims/{claim}", path: "/api/v1/claims/plan"}
	bodies := [][]byte{[]byte("one"), []byte("two"), []byte("three")}
	for i, body := range bodies {
		segment := logSegment(i+1, body)
		err := uploader.PutSegment(t.Context(), segment, body)
		if i < 2 && err == nil {
			t.Fatalf("segment %d reported success although its response was lost", i+1)
		}
		if err != nil {
			if err := uploader.PutSegment(t.Context(), segment, body); err != nil {
				t.Fatalf("segment %d did not recover: %v", i+1, err)
			}
		}
	}
	manifest := logseg.Manifest{Subject: &subject, Status: logseg.StatusComplete}
	if err := uploader.PutManifest(t.Context(), manifest); err == nil {
		t.Fatal("manifest reported success although its upload response was lost")
	}
	if err := uploader.PutManifest(t.Context(), manifest); err != nil {
		t.Fatalf("manifest did not recover after a create-only refusal: %v", err)
	}
	if !store.sealed || store.puts != 4 || store.refused != 2 || len(uploader.segments) != 3 {
		t.Fatalf("sealed=%v puts=%d refused=%d segments=%d; want every object stored once and the stream sealed", store.sealed, store.puts, store.refused, len(uploader.segments))
	}
	if !bytes.Equal(store.objects["3"], bodies[2]) {
		t.Fatal("a later segment was blocked behind a recovered one")
	}
}

// A create-only refusal proves only that a key exists: when the stored object differs, the commit refuses it.
func TestNativeLogUploaderRefusesAnExistingObjectWithOtherBytes(t *testing.T) {
	subject := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "team", RunID: "run", Unit: &blob.LogUnit{Kind: "plan", Generation: 1}}
	store := &fakeLogStore{t: t, subject: subject, objects: map[string][]byte{"1": []byte("other")}, committed: map[int64]bool{}}
	uploader := &nativeLogUploader{transport: testEngine(t, store.handle), subject: subject, pattern: "/api/v1/claims/{claim}", path: "/api/v1/claims/plan"}
	body := []byte("mine")
	if err := uploader.PutSegment(t.Context(), logSegment(1, body), body); err == nil || store.commits != 0 || len(uploader.segments) != 0 {
		t.Fatalf("err=%v commits=%d; want the differing object refused", err, store.commits)
	}
}

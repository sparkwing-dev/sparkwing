package client

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestNativeLogCommitRequiresAcknowledgement(t *testing.T) {
	for _, acknowledged := range []bool{false, true} {
		t.Run(map[bool]string{false: "refused", true: "recorded"}[acknowledged], func(t *testing.T) {
			subject := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "team", RunID: "run", Unit: &blob.LogUnit{Kind: "plan", Generation: 1}}
			called := false
			transport := testEngine(t, func(request *http.Request) (*http.Response, error) {
				called = true
				if request.Method != "POST" || request.URL.Path != "/api/v1/claims/plan/log-manifest" {
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
			err := uploader.commit(t.Context(), nil, true, "")
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
				if request.Method != "POST" || request.URL.Path != path || request.Header.Get("Authorization") != "Bearer "+raw {
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
			if err := uploader.(*nativeLogUploader).commit(t.Context(), nil, true, "tail lost"); err != nil || calls != 1 {
				t.Fatalf("log commit lost its bound bearer: calls=%d error=%v", calls, err)
			}
			p.Capability.Digest = sha256.Sum256([]byte("another-award"))
			if _, err := host.LogUploader(context.Background(), p, subject); err == nil || calls != 1 {
				t.Fatal("changed bearer reached log persistence")
			}
		})
	}
}

package client

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func TestNativeLogUploaderEmptyRegistrationRequiresAcknowledgement(t *testing.T) {
	for _, acknowledged := range []bool{false, true} {
		t.Run(map[bool]string{false: "refused", true: "recorded"}[acknowledged], func(t *testing.T) {
			subject := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "team", RunID: "run", Unit: &blob.LogUnit{Kind: "plan", Generation: 1}}
			called := false
			transport := testEngine(t, func(request *http.Request) (*http.Response, error) {
				called = true
				if request.Method != "POST" || request.URL.Path != "/api/v1/claims/plan/log-manifest" {
					t.Fatalf("unexpected registration route: %s %s", request.Method, request.URL.Path)
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
				if body.Subject.RunID != subject.RunID || len(body.Segments) != 0 || body.Final || body.Reason != "" {
					t.Fatalf("invalid empty registration: %+v", body)
				}
				if acknowledged {
					return engineResponse(200, `{"recorded":true}`), nil
				}
				return engineResponse(200, `{"recorded":false}`), nil
			})
			uploader := &nativeLogUploader{transport: transport, subject: subject, pattern: "/api/v1/claims/{claim}", path: "/api/v1/claims/plan"}
			err := uploader.commit(t.Context(), nil, false, "")
			if !called || (err == nil) != acknowledged {
				t.Fatalf("registration acknowledgement lost: called=%v error=%v", called, err)
			}
		})
	}
}

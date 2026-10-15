package nodehost_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func nodeRow(status, outcome string) http.HandlerFunc {
	return answer(http.StatusOK, map[string]string{"id": "x", "status": status, "outcome": outcome})
}

func outputPath(node string) string {
	return "/node/v1/jobs/" + url.PathEscape(node) + "/output"
}

func TestOutputReadsANodeOfTheSameRun(t *testing.T) {
	s, logs := start(t)
	c := newController(t)
	grant := c.object("build-out", []byte(`{"digest":"sha256:abc"}`))
	c.handle("GET /api/v1/runs/run-1/nodes/{node}", nodeRow("done", "success"))
	c.handle("GET /api/v1/runs/run-1/nodes/{node}/output", answer(http.StatusOK, grant))
	bearer, _ := admitClaim(t, s, "deploy", claimFor(t, c))

	got := ask(t, s, http.MethodGet, "/node/v1/jobs/build%2Flinux/output", bearer, "")
	if got.status != http.StatusOK || got.body["output"].(map[string]any)["digest"] != "sha256:abc" {
		t.Fatalf("status %d body %v", got.status, got.body)
	}
	asked := c.asked()
	if asked[0] != "GET /api/v1/runs/run-1/nodes/build%2Flinux" {
		t.Fatalf("forwarded %v", asked)
	}
	noLeak(t, logs)
}

func TestOutputRefusals(t *testing.T) {
	cases := []struct {
		name, node     string
		row, out       http.HandlerFunc
		status         int
		code           string
		reachesControl bool
	}{
		{
			name: "unknown", node: "nope", row: answer(http.StatusNotFound, map[string]string{"error": "not found"}),
			status: http.StatusNotFound, code: "unknown_job", reachesControl: true,
		},
		{
			name: "running", node: "build", row: nodeRow("running", ""),
			status: http.StatusConflict, code: "conflict", reachesControl: true,
		},
		{
			name: "failed", node: "build", row: nodeRow("done", "failed"),
			status: http.StatusNotFound, code: "not_found", reachesControl: true,
		},
		{
			name: "not a dependency", node: "build", row: nodeRow("done", "success"),
			out:    answer(http.StatusForbidden, map[string]string{"error": "output_not_ancestor"}),
			status: http.StatusForbidden, code: "forbidden", reachesControl: true,
		},
		{
			name: "claim ended", node: "build", row: answer(http.StatusForbidden, map[string]string{"error": "claim_ended"}),
			status: http.StatusServiceUnavailable, code: "unavailable", reachesControl: true,
		},
		{
			name: "controller error", node: "build", row: leak,
			status: http.StatusServiceUnavailable, code: "unavailable", reachesControl: true,
		},
		{
			name: "corrupt object", node: "build", row: nodeRow("done", "success"),
			out:    answer(http.StatusOK, store.OutputReadGrant{URL: "/objects/x", SHA256: "00", Size: 2}),
			status: http.StatusServiceUnavailable, code: "unavailable", reachesControl: true,
		},
		{name: "dot-dot", node: "../run-2/nodes/x", status: http.StatusBadRequest, code: "bad_request"},
		{name: "backslash", node: `a\b`, status: http.StatusBadRequest, code: "bad_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, logs := start(t)
			c := newController(t)
			c.object("x", []byte("{}"))
			if tc.row != nil {
				c.handle("GET /api/v1/runs/run-1/nodes/{node}", tc.row)
			}
			if tc.out != nil {
				c.handle("GET /api/v1/runs/run-1/nodes/{node}/output", tc.out)
			}
			bearer, _ := admitClaim(t, s, "deploy", claimFor(t, c))
			wantRefusal(t, ask(t, s, http.MethodGet, outputPath(tc.node), bearer, ""), tc.status, tc.code)
			if reached := len(c.asked()) > 0; reached != tc.reachesControl {
				t.Fatalf("controller asked %v", c.asked())
			}
			noLeak(t, logs)
		})
	}
}

func TestPipelineLatestReadsADeclaredReference(t *testing.T) {
	s, logs := start(t)
	c := newController(t)
	grant := c.object("ref", []byte(`"v1.2.3"`))
	var asked store.ClaimInputRequest
	c.handle("POST /api/v1/runs/run-1/nodes/deploy/claim/input", func(w http.ResponseWriter, r *http.Request) {
		if !decodeJSON(r, &asked) {
			t.Error("claim input body is not JSON")
		}
		answer(http.StatusOK, store.ClaimInput{RunID: "run-0", NodeID: "tag", Output: grant})(w, r)
	})
	bearer, _ := admitClaim(t, s, "deploy", claimFor(t, c))

	got := ask(t, s, http.MethodGet, "/node/v1/pipelines/release/latest?job=tag&max_age_ms=5000", bearer, "")
	if got.status != http.StatusOK || got.body["run_id"] != "run-0" || got.body["output"] != "v1.2.3" {
		t.Fatalf("status %d body %v", got.status, got.body)
	}
	want := store.ClaimInputRequest{Kind: store.ClaimInputLastRun, Pipeline: "release", Node: "tag", MaxAgeMS: 5000}
	if asked != want {
		t.Fatalf("forwarded %+v, want %+v", asked, want)
	}
	noLeak(t, logs)
}

func TestPipelineLatestRefusals(t *testing.T) {
	cases := []struct {
		name, query string
		input       http.HandlerFunc
		status      int
		code        string
	}{
		{"no run", "?job=tag", answer(http.StatusNotFound, map[string]string{"error": "not found"}), http.StatusNotFound, "not_found"},
		{
			"undeclared", "?job=tag", answer(http.StatusForbidden, map[string]string{"error": "input_undeclared"}),
			http.StatusForbidden, "forbidden",
		},
		{"no output", "?job=tag", answer(http.StatusOK, store.ClaimInput{RunID: "run-0"}), http.StatusNotFound, "not_found"},
		{"controller error", "?job=tag", leak, http.StatusServiceUnavailable, "unavailable"},
		{"no node", "", nil, http.StatusBadRequest, "bad_request"},
		{"negative age", "?job=tag&max_age_ms=-1", nil, http.StatusBadRequest, "bad_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, logs := start(t)
			c := newController(t)
			if tc.input != nil {
				c.handle("POST /api/v1/runs/run-1/nodes/deploy/claim/input", tc.input)
			}
			bearer, _ := admitClaim(t, s, "deploy", claimFor(t, c))
			wantRefusal(t, ask(t, s, http.MethodGet, "/node/v1/pipelines/release/latest"+tc.query, bearer, ""), tc.status, tc.code)
			if tc.input == nil && len(c.asked()) > 0 {
				t.Fatalf("a malformed request reached the controller: %v", c.asked())
			}
			noLeak(t, logs)
		})
	}
}

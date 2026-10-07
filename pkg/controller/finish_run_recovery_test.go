package controller

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestFinishRunRetryReportsCommittedOutcomeWithoutRefolding(t *testing.T) {
	for _, failedFold := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit before followups", true: "profile write failed"}[failedFold], func(t *testing.T) {
			st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			if err := st.CreateTrigger(t.Context(), store.Trigger{ID: "run", Pipeline: "profile", TriggerSource: "github", Repo: "example/project", GithubOwner: "example", GithubRepo: "project", GithubRepoID: 701, TriggerEnv: map[string]string{envGitHubAppInstallation: "7", sparkwing.EnvGitHubEventName: sparkwing.EventPullRequest, sparkwing.EnvPRHeadSHA: strings.Repeat("1", 40)}, CreatedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if err := st.CreateRun(t.Context(), store.Run{ID: "run", Pipeline: "profile", Status: "running", StartedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if err := st.CreateNode(t.Context(), store.Node{RunID: "run", NodeID: "build", Status: "pending"}); err != nil {
				t.Fatal(err)
			}
			if err := st.FinishNode(t.Context(), "run", "build", "success", "", nil); err != nil {
				t.Fatal(err)
			}
			if err := st.AddNodeMetricSample(t.Context(), "run", "build", store.MetricSample{Kind: store.MetricInterval, TS: time.Now(), CPUMillicores: 500, MemoryBytes: 200}); err != nil {
				t.Fatal(err)
			}
			s := New(st, nil)
			finish := func(status string, want int) {
				t.Helper()
				req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run/finish", strings.NewReader(`{"status":"`+status+`"}`))
				req.SetPathValue("id", "run")
				req.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				s.handleFinishRun(response, req)
				if response.Code != want {
					t.Fatalf("finish status=%d body=%s", response.Code, response.Body.String())
				}
			}
			if failedFold {
				if _, err := st.DB().Exec(`CREATE TRIGGER reject_profile BEFORE INSERT ON pipeline_profiles BEGIN SELECT RAISE(FAIL, 'profile storage unavailable'); END`); err != nil {
					t.Fatal(err)
				}
				finish("success", http.StatusNoContent)
				if _, err := st.DB().Exec(`DROP TRIGGER reject_profile`); err != nil {
					t.Fatal(err)
				}
			} else if won, err := st.FinishRunIfActive(t.Context(), "run", "success", ""); err != nil || !won {
				t.Fatalf("commit boundary won=%t error=%v", won, err)
			}
			github := attachAppChecks(t, s, "")
			s.reportGitHubRunState(t.Context(), "run", "pending")
			drainChecks(t, s)
			if calls := github.CheckRunCalls(); len(calls) != 1 || calls[0].Status != "queued" {
				t.Fatalf("check run writes = %+v, want one queued create", calls)
			}
			finish("failed", http.StatusBadRequest)
			drainChecks(t, s)
			calls := github.CheckRunCalls()
			if last := calls[len(calls)-1]; last.Status != "completed" || last.Conclusion != "success" {
				t.Fatalf("check run writes = %+v, want the committed success", calls)
			}
			for _, node := range []string{"", "build"} {
				profile, err := st.GetPipelineProfile(t.Context(), "profile", node)
				if err != nil || (profile != nil && profile.SampleCount != 0) {
					t.Fatalf("retry folded after terminal commit: profile=%+v error=%v", profile, err)
				}
			}
		})
	}
}

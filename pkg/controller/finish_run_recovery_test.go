package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestFinishRunRetryReportsCommittedOutcomeWithoutRefolding(t *testing.T) {
	for _, failedFold := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit before followups", true: "profile write failed"}[failedFold], func(t *testing.T) {
			st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			if err := st.CreateTrigger(t.Context(), store.Trigger{ID: "run", Pipeline: "profile", TriggerSource: "github", Repo: "example/project", GithubOwner: "example", GithubRepo: "project", TriggerEnv: map[string]string{sparkwing.EnvGitHubEventName: sparkwing.EventPullRequest, sparkwing.EnvPRHeadSHA: strings.Repeat("1", 40)}, CreatedAt: time.Now()}); err != nil {
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
			finish := func(status string) {
				t.Helper()
				req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run/finish", strings.NewReader(`{"status":"`+status+`"}`))
				req.SetPathValue("id", "run")
				req.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				s.handleFinishRun(response, req)
				if response.Code != http.StatusNoContent {
					t.Fatalf("finish status=%d body=%s", response.Code, response.Body.String())
				}
			}
			if failedFold {
				if _, err := st.DB().Exec(`CREATE TRIGGER reject_profile BEFORE INSERT ON pipeline_profiles BEGIN SELECT RAISE(FAIL, 'profile storage unavailable'); END`); err != nil {
					t.Fatal(err)
				}
				finish("success")
				if _, err := st.DB().Exec(`DROP TRIGGER reject_profile`); err != nil {
					t.Fatal(err)
				}
			} else if won, err := st.FinishRunIfActive(t.Context(), "run", "success", ""); err != nil || !won {
				t.Fatalf("commit boundary won=%t error=%v", won, err)
			}
			posted := make(chan string, 1)
			github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct{ State string }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				posted <- body.State
				w.WriteHeader(http.StatusCreated)
			}))
			t.Cleanup(github.Close)
			s.githubCommitStatuses = newGitHubCommitStatusReporter("test-token", "", github.URL, github.Client())
			t.Cleanup(s.githubCommitStatuses.stop)
			waiting, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			t.Cleanup(cancel)
			s.reportGitHubCommitStatus(t.Context(), "run", "pending")
			select {
			case status := <-posted:
				if status != "pending" {
					t.Fatalf("initial status=%q; want pending", status)
				}
			case <-waiting.Done():
				t.Fatal("reporter did not register the accepted run")
			}
			finish("failed")
			select {
			case status := <-posted:
				if status != "success" {
					t.Fatalf("reported %q; want committed success", status)
				}
			case <-waiting.Done():
				t.Fatal("retry did not recover commit-status reporting")
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

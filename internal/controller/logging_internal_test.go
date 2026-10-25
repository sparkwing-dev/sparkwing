package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp/githubapptest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type credentialLogValue string

func (v credentialLogValue) LogValue() slog.Value {
	return slog.GroupValue(slog.String("resolved", string(v)))
}

func TestControllerLogsFormatCredentials(t *testing.T) {
	for _, prefix := range capability.CredentialPrefixes() {
		t.Run(prefix, func(t *testing.T) {
			credential := prefix + strings.Repeat("x", 43)
			digest := sha256.Sum256([]byte(credential))
			fingerprint := fmt.Sprintf("[credential:%x]", digest[:8])
			for _, format := range []string{"text", "json"} {
				var logs bytes.Buffer
				var handler slog.Handler = slog.NewTextHandler(&logs, nil)
				if format == "json" {
					handler = slog.NewJSONHandler(&logs, nil)
				}
				logger := New(nil, slog.New(handler)).logger
				logger = logger.With("attached", credential).WithGroup("outer").With("nested", credential)
				logger.Info("message "+credential,
					"plain", credential, "bytes", []byte(credential), "err", fmt.Errorf("failed: %s", credential),
					"list", []string{"safe", credential}, "object", map[string]any{"token": credential},
					"lazy", credentialLogValue(credential),
					credential, credential,
					slog.Group("group", "inside", credential), "count", 42)
				out := logs.String()
				if strings.Contains(out, credential) || strings.Contains(out, prefix+"x") || strings.Count(out, fingerprint) != 12 {
					t.Errorf("%s log did not fingerprint every credential: %s", format, out)
				}
				if !strings.Contains(out, "42") {
					t.Errorf("lost numeric attribute: %s", out)
				}
			}
		})
	}
}

func TestIdleClaimShedNeverLogsRunnerCredential(t *testing.T) {
	var logs bytes.Buffer
	s := New(nil, slog.New(slog.NewJSONHandler(&logs, nil))).WithIdleClaimPoll(time.Second).WithIdleClaimPollEnforced(true)
	s.recordQueueActivity(time.Now().Add(-time.Hour))
	credential := "swk_" + strings.Repeat("x", 43)
	r := httptest.NewRequest("POST", "/api/v1/claims", nil)
	r.Header.Set(store.RunnerIdentityHeader, credential)
	key, ok := s.runnerBudgetKey(httptest.NewRecorder(), r, capability.Authorized{})
	if !ok || !s.admitIdleClaimPoll(httptest.NewRecorder(), r, key) {
		t.Fatal("first poll refused")
	}
	if s.admitIdleClaimPoll(httptest.NewRecorder(), r, key) {
		t.Fatal("second poll admitted")
	}
	if !strings.Contains(logs.String(), "claim shed") || strings.Contains(logs.String(), credential) || !strings.Contains(logs.String(), "[credential:") {
		t.Fatalf("unsafe claim warning: %s", &logs)
	}
}

func TestGitHubMutationLogsNeverContainCredentials(t *testing.T) {
	for _, path := range []string{"delivery", "pipeline"} {
		t.Run(path, func(t *testing.T) {
			s, raw, csrf := reviewAccount(t, "owner")
			var logs bytes.Buffer
			s.logger = New(nil, slog.New(slog.NewJSONHandler(&logs, nil))).logger
			s.githubApp = &githubAppState{client: githubapp.New(githubapp.Config{AppID: 99, WebhookSecret: "test-key"})}
			credential := "swk_" + strings.Repeat("x", 43)
			engineSQL(t, s.store, `INSERT INTO github_app_installations(installation_id,team,account_id,account_login,account_type,created_at,updated_at) VALUES (7,'a',42,'acme','Organization',1,1)`)
			message := "github app installation uninstalled"
			if path == "delivery" {
				body := []byte(`{"action":"deleted","installation":{"id":7,"app_id":99}}`)
				r := httptest.NewRequest("POST", "/webhooks/github-app", bytes.NewReader(body))
				r.Header.Set("X-GitHub-Event", "installation")
				r.Header.Set("X-GitHub-Delivery", credential)
				r.Header.Set("X-Hub-Signature-256", githubapptest.SignWith("test-key", body))
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, r)
				if w.Code != 202 {
					t.Fatalf("enqueue = %d %s", w.Code, w.Body)
				}
				outcome, err := s.processNextGitHubDelivery(t.Context())
				if err != nil || outcome == nil || outcome.status != 200 {
					t.Fatalf("uninstall = %+v %v", outcome, err)
				}
			} else {
				engineSQL(t, s.store, `INSERT INTO github_app_triggers(team,repository_id,pipeline,repository,installation_id,created_at,repo_key) VALUES ('a',701,?,'acme/repo',7,1,'gh:701')`, credential)
				reviewSessionRequest(t, s, raw, csrf, "DELETE", "/api/v1/team/github-app/triggers?repository_id=701&pipeline="+credential, "", 204)
				message = "github app trigger removed"
			}
			if len(auditLines(t, &logs, message)) != 1 || strings.Contains(logs.String(), credential) || !strings.Contains(logs.String(), "[credential:") {
				t.Fatalf("unsafe %s log: %s", path, &logs)
			}
		})
	}
}

func TestControllerLogsKeepTokenPrefixListBound(t *testing.T) {
	var logs bytes.Buffer
	s := New(nil, slog.New(slog.NewJSONHandler(&logs, nil)))
	prefixes := strings.Repeat("swk_0123456789ab,", 20)
	s.logger.Info("audit", "machine_id", "worker", "token_prefixes", prefixes)
	records := auditLines(t, &logs, "audit")
	if len(records) != 1 || records[0]["machine_id"] != "worker" || records[0]["token_prefixes"] != prefixes[:maxAuditValue] {
		t.Fatalf("machine removal prefix list lost its bound: %s", &logs)
	}
}

func TestControllerLogsBoundStringsAfterCredentialFormatting(t *testing.T) {
	var logs bytes.Buffer
	s := New(nil, slog.New(slog.NewJSONHandler(&logs, nil)))
	// A credential crossing the limit must be fingerprinted before truncation.
	value := strings.Repeat("a", 240) + "swk_" + strings.Repeat("x", 43) + "/" + strings.Repeat("z", 1<<20)
	s.logger.With("attached", value).WithGroup("outer").Info(value,
		"plain", value, "bytes", []byte(value), "err", fmt.Errorf("failed: %s", value),
		"list", []string{value}, "lazy", credentialLogValue(value),
		slog.Group("group", "inside", value), "unicode", strings.Repeat("界", 100))
	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	var check func(any, int)
	check = func(v any, limit int) {
		switch v := v.(type) {
		case string:
			if len(v) > limit || !strings.HasSuffix(v, "[truncated]") || !utf8.ValidString(v) || strings.Contains(v, "swk_x") {
				t.Errorf("unsafe or unbounded string: length=%d, limit=%d", len(v), limit)
			}
		case map[string]any:
			for _, child := range v {
				check(child, limit)
			}
		case []any:
			for _, child := range v {
				check(child, limit)
			}
		}
	}
	check(record["msg"], 1024)
	check(record["attached"], 256)
	check(record["outer"], 256)
}

func BenchmarkControllerLog(b *testing.B) {
	for _, filtered := range []bool{false, true} {
		b.Run(fmt.Sprint(filtered), func(b *testing.B) {
			var handler slog.Handler = slog.NewJSONHandler(io.Discard, nil)
			if filtered {
				handler = CredentialSafeLogHandler(handler)
			}
			logger := slog.New(handler)
			b.ReportAllocs()
			for b.Loop() {
				logger.Info("http", "route", "/api/v1/runs", "status", 200)
			}
		})
	}
}

package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestMaskEventPayload_RedactsForwardedSecrets(t *testing.T) {
	m := secrets.NewMasker()
	m.Register("s3cr3t-token-value")

	payload := []byte(`{"child_run_id":"c1","args":{"token":"s3cr3t-token-value","env":"prod"}}`)
	got := string(m.MaskJSON(payload))

	if strings.Contains(got, "s3cr3t-token-value") {
		t.Errorf("payload still carries the secret: %s", got)
	}
	if !strings.Contains(got, "***") {
		t.Errorf("payload carries no redaction marker: %s", got)
	}
	if !strings.Contains(got, `"env":"prod"`) {
		t.Errorf("non-secret content was damaged: %s", got)
	}
}

func TestMaskEventPayload_CoversEmbeddedSecrets(t *testing.T) {
	m := secrets.NewMasker()
	m.Register("s3cr3t")
	got := string(m.MaskJSON([]byte(`{"args":{"url":"https://x/?t=s3cr3t"}}`)))
	if strings.Contains(got, "s3cr3t") {
		t.Errorf("embedded secret survived: %s", got)
	}
}

func TestMaskEventPayload_CanonicalWithoutSecrets(t *testing.T) {
	payload := []byte(`{"child_run_id":"c1","args":{"env":"prod"}}`)
	want := `{"args":{"env":"prod"},"child_run_id":"c1"}`
	if got := secrets.NewMasker().MaskJSON(payload); string(got) != want {
		t.Errorf("payload changed: %s", got)
	}
	if got := (*secrets.Masker)(nil).MaskJSON(payload); string(got) != want {
		t.Errorf("nil masker changed the payload: %s", got)
	}
}

func TestEscapedChildEventSecretsStayMaskedInStoreAndReaderHTTP(t *testing.T) {
	for _, secret := range []string{"audit<token>&value", "audit\"token", "audit\\token", "audit-line-one\naudit-line-two", "audit-雪-\u2028-token", "ordinary-private-token"} {
		t.Run(secret, func(t *testing.T) {
			m := secrets.NewMasker()
			m.Register(secret)
			st, err := teststore.Open(filepath.Join(t.TempDir(), "events.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			ctx := context.Background()
			if err := st.CreateRun(ctx, store.Run{ID: "parent", Pipeline: "parent", Status: "success"}); err != nil {
				t.Fatal(err)
			}
			for _, event := range []struct {
				kind  string
				attrs map[string]any
			}{
				{"child_run_start", map[string]any{"args": map[string]string{"token": secret}}},
				{"child_run_finish", map[string]any{"pipeline": "child-" + secret, "status": "success"}},
				{"child_run_finish", map[string]any{"error": "failure: " + secret, "status": "failed"}},
			} {
				event.attrs["sequence"], event.attrs["ok"], event.attrs["empty"], event.attrs["safe"] = json.Number("9007199254740993"), true, nil, "unchanged"
				raw, err := json.Marshal(event.attrs)
				if err != nil {
					t.Fatal(err)
				}
				if err := st.AppendEvent(ctx, "parent", "", event.kind, m.MaskJSON(raw)); err != nil {
					t.Fatal(err)
				}
			}
			assertPayloads := func(events []store.Event) {
				t.Helper()
				if len(events) != 3 {
					t.Fatalf("events=%d", len(events))
				}
				for _, event := range events {
					var value map[string]any
					decoder := json.NewDecoder(bytes.NewReader(event.Payload))
					decoder.UseNumber()
					if err := decoder.Decode(&value); err != nil {
						t.Fatal(err)
					}
					switch {
					case event.Kind == "child_run_start":
						if value["args"].(map[string]any)["token"] != "***" {
							t.Error("start contains recoverable secret")
						}
					case value["status"] == "success":
						if value["pipeline"] != "child-***" {
							t.Error("finish contains recoverable secret")
						}
					case value["status"] == "failed":
						if value["error"] != "failure: ***" {
							t.Error("error contains recoverable secret")
						}
					default:
						t.Fatal("event structure changed")
					}
					if value["sequence"] != json.Number("9007199254740993") || value["ok"] != true || value["empty"] != nil || value["safe"] != "unchanged" {
						t.Errorf("nonsecret values changed: %+v", value)
					}
				}
			}
			events, err := st.ListEventsAfter(ctx, "parent", 0, 10)
			if err != nil {
				t.Fatal(err)
			}
			assertPayloads(events)
			raw, _, err := st.CreateToken("reader", store.TokenKindUser, []string{controller.ScopeRunsRead}, 0, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			srv := controller.New(st, nil)
			t.Cleanup(func() { _ = srv.Shutdown(ctx) })
			req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/parent/events", nil)
			req.Header.Set("Authorization", "Bearer "+raw)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("reader HTTP=%d", rec.Code)
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &events); err != nil {
				t.Fatal(err)
			}
			assertPayloads(events)
		})
	}
}

func TestMaskEventPayloadPreservesCanonicalAndPlainFallback(t *testing.T) {
	m := secrets.NewMasker()
	m.Register("plain-private-token")
	for raw, want := range map[string]string{
		`{ "value": 9007199254740993, "safe": true }`: `{"safe":true,"value":9007199254740993}`,
		"ordinary plain bytes":                        "ordinary plain bytes",
	} {
		if got := m.MaskJSON([]byte(raw)); string(got) != want {
			t.Fatalf("payload = %s, want %s", got, want)
		}
	}
	if got := string(m.MaskJSON([]byte("invalid JSON plain-private-token"))); got != "invalid JSON ***" {
		t.Fatalf("plain fallback=%q", got)
	}
}

// A payload is masked as a log record's attrs are: a numeric secret is masked
// in keys and numbers too, and the payload stays JSON the event reader serves.
func TestMaskEventPayloadMasksNumericSecretsAsLogAttrs(t *testing.T) {
	m := secrets.NewMasker()
	m.Register("12345")
	payload := m.MaskJSON([]byte(`{"count":12345,"large":9007199254740993,"12345":"safe"}`))
	var value map[string]string
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatalf("masked payload is not JSON: %s", payload)
	}
	if strings.Contains(string(payload), "12345,") || !strings.Contains(value["masked_attrs"], "9007199254740993") {
		t.Fatalf("masked payload = %s", payload)
	}
}

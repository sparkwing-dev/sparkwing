package controller_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// hack: wrapping the reader hides its length from the transport, which is how
// a test forces the chunked encoding the server sees as ContentLength == -1.
type unsizedBody struct{ io.Reader }

func postChunked(t *testing.T, url, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, unsizedBody{strings.NewReader(body)})
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

func TestClaimTrigger_ChunkedBodyKeepsThePipelineFilter(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.CreateTrigger(context.Background(), store.Trigger{
		ID: "run-other", Pipeline: "other", TriggerSource: "cli", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	resp := postChunked(t, srv.URL+"/api/v1/triggers/claim", "", `{"pipelines":["only-this-one"]}`)
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status=%d body=%s, want 204: the filter excluded the only queued trigger", resp.StatusCode, raw)
	}

	trig, err := st.GetTrigger(context.Background(), "run-other")
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if trig.Status != "pending" {
		t.Errorf("trigger status=%q, want pending: it was claimed despite the filter", trig.Status)
	}
}

func TestClaimTrigger_EmptyChunkedBodyStillClaims(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.CreateTrigger(context.Background(), store.Trigger{
		ID: "run-any", Pipeline: "other", TriggerSource: "cli", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	resp := postChunked(t, srv.URL+"/api/v1/triggers/claim", "", "")
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200 for an unfiltered claim", resp.StatusCode, raw)
	}
}

func TestRotateToken_ChunkedBodyKeepsTheGrace(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	now := time.Now().UTC()
	admin, _, err := st.CreateToken("root", store.TokenKindUser,
		[]string{controller.ScopeAdmin}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken admin: %v", err)
	}
	_, worker, err := st.CreateToken("worker", store.TokenKindRunner,
		[]string{controller.ScopeTriggersClaim}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken worker: %v", err)
	}

	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	defer srv.Close()

	resp := postChunked(t, srv.URL+"/api/v1/tokens/"+worker.Prefix+"/rotate", admin, `{"grace_secs":3600}`)
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", resp.StatusCode, raw)
	}
	var body struct {
		OldRevoked int64 `json:"old_revoked_at"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	grace := time.Unix(body.OldRevoked, 0).Sub(now)
	if grace > 2*time.Hour {
		t.Errorf("old token revoked in %s, want the hour the chunked body asked for", grace)
	}
}

func TestClaimSpecificTrigger_ChunkedBodyKeepsTheLease(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.CreateTrigger(context.Background(), store.Trigger{
		ID: "run-specific", Pipeline: "child", TriggerSource: "cli", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()

	resp := postChunked(t, srv.URL+"/api/v1/triggers/run-specific/claim", "",
		`{"lease_nanos":`+strconv.FormatInt(int64(time.Hour), 10)+`}`)
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", resp.StatusCode, raw)
	}

	trig, err := st.GetTrigger(context.Background(), "run-specific")
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if trig.LeaseExpiresAt == nil {
		t.Fatal("the claim recorded no lease")
	}
	if lease := time.Until(*trig.LeaseExpiresAt); lease <= store.DefaultLeaseDuration {
		t.Errorf("lease=%s, want the hour the chunked body asked for rather than the %s default",
			lease.Round(time.Second), store.DefaultLeaseDuration)
	}
}

func TestReconcileOrphans_ChunkedBodyKeepsTheThreshold(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	admin, _, err := st.CreateToken("root", store.TokenKindUser,
		[]string{controller.ScopeAdmin}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken admin: %v", err)
	}

	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	defer srv.Close()

	resp := postChunked(t, srv.URL+"/api/v1/maintenance/reconcile-orphans", admin,
		`{"threshold_nanos":`+strconv.FormatInt(int64(time.Hour), 10)+`}`)
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200: the threshold the chunked body carried was dropped",
			resp.StatusCode, raw)
	}
}

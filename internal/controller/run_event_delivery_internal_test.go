package controller

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runevents"
	"github.com/sparkwing-dev/sparkwing/internal/runevents/webhook"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

var deliverySecret = "whsec_" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))

type received struct {
	id, timestamp string
	body          []byte
}

type receiver struct {
	t       *testing.T
	mu      sync.Mutex
	got     []received
	replies []int
	header  http.Header
	srv     *httptest.Server
}

func newReceiver(t *testing.T, replies ...int) *receiver {
	r := &receiver{t: t, replies: replies}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		id, ts := req.Header.Get("webhook-id"), req.Header.Get("webhook-timestamp")
		key, _ := base64.StdEncoding.DecodeString(deliverySecret[len("whsec_"):])
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(id + "." + ts + "."))
		mac.Write(body)
		if want := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil)); !hmac.Equal([]byte(req.Header.Get("webhook-signature")), []byte(want)) {
			t.Errorf("signature does not verify")
		}
		r.mu.Lock()
		r.got = append(r.got, received{id, ts, body})
		status := r.replies[min(len(r.got), len(r.replies))-1]
		for k, v := range r.header {
			w.Header()[k] = v
		}
		r.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) requests() []received {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]received(nil), r.got...)
}

type deliveryHarness struct {
	t   *testing.T
	st  *store.Store
	mu  sync.Mutex
	now time.Time
	d   *runEventDeliverer
	enc *secrets.Cipher
}

func newDeliveryHarness(t *testing.T, policy webhook.Policy) *deliveryHarness {
	t.Helper()
	h := &deliveryHarness{t: t, now: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)}
	st, err := teststore.Open(t, filepath.Join(t.TempDir(), "state.db"), teststore.WithClock(h.clock))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	h.st = st
	cipher, err := secrets.NewCipher(bytes.Repeat([]byte{7}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	h.enc = cipher
	if err := teststore.SeedRun(st, t.Context(), store.DefaultTeam, store.Run{RunSummary: runstate.RunSummary{ID: "run", Pipeline: "p", Status: "success", StartedAt: h.now}}); err != nil {
		t.Fatal(err)
	}
	h.d = newRunEventDeliverer(st.AsOperator(), cipher, policy, slog.New(slog.DiscardHandler))
	h.d.now = h.clock
	h.d.jitter = func(n int64) int64 { return n - 1 }
	return h
}

func (h *deliveryHarness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func (h *deliveryHarness) advanceTo(at time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.now = at
}

func (h *deliveryHarness) subscribe(id, url, secret string, due time.Time) {
	h.t.Helper()
	sealed, err := h.enc.SealBound(secrets.PurposeEventSigning, string(store.DefaultTeam), store.RunEventSecretName(id), "", false, true, secret)
	if err != nil {
		h.t.Fatal(err)
	}
	ctx := h.t.Context()
	if err := teststore.SeedRunEventSubscription(ctx, h.st, store.DefaultTeam, id, url, sealed, runevents.Filter{EventTypes: []string{runevents.EventFinished}}); err != nil {
		h.t.Fatal(err)
	}
	if err := teststore.SeedRunEventDelivery(ctx, h.st, store.DefaultTeam, id, "run/2", "run", []byte(`{"id":"run/2"}`), due); err != nil {
		h.t.Fatal(err)
	}
}

func (h *deliveryHarness) step() {
	h.t.Helper()
	if err := h.d.tick(h.t.Context()); err != nil {
		h.t.Fatal(err)
	}
	h.d.inflight.Wait()
}

func (h *deliveryHarness) row(sub string) teststore.RunEventDeliveryRow {
	h.t.Helper()
	rows, err := teststore.RunEventDeliveries(h.t.Context(), h.st, store.DefaultTeam)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, r := range rows {
		if r.SubscriptionID == sub {
			return r
		}
	}
	h.t.Fatalf("no delivery for %s", sub)
	return teststore.RunEventDeliveryRow{}
}

func TestRunEventDeliveryRetriesOnScheduleWithTheSameEvent(t *testing.T) {
	h := newDeliveryHarness(t, webhook.Policy{Local: true})
	rx := newReceiver(t, 500, 503, 200)
	t0 := h.clock()
	h.subscribe("sub_a", rx.srv.URL+"/hook", deliverySecret, t0)

	h.step()
	if r := h.row("sub_a"); r.State != "pending" || r.Attempts != 1 || !r.NextAt.Equal(t0.Add(10*time.Second)) || r.LastStatus.Int64 != 500 || r.LastError != "http_status" {
		t.Fatalf("after first failure %+v", r)
	}
	h.advanceTo(t0.Add(9 * time.Second))
	h.step()
	if n := len(rx.requests()); n != 1 {
		t.Fatalf("retried before next_at: %d requests", n)
	}
	h.advanceTo(t0.Add(10 * time.Second))
	h.step()
	if r := h.row("sub_a"); r.State != "pending" || r.Attempts != 2 || !r.NextAt.Equal(t0.Add(40*time.Second)) || r.LastStatus.Int64 != 503 {
		t.Fatalf("after second failure %+v", r)
	}
	h.advanceTo(t0.Add(40 * time.Second))
	h.step()
	if r := h.row("sub_a"); r.State != "succeeded" || r.Attempts != 3 || r.LastStatus.Int64 != 200 || !r.Finished {
		t.Fatalf("after delivery %+v", r)
	}
	got := rx.requests()
	if len(got) != 3 {
		t.Fatalf("receiver saw %d requests", len(got))
	}
	for i, offset := range []time.Duration{0, 10 * time.Second, 40 * time.Second} {
		if got[i].id != "run/2" || string(got[i].body) != `{"id":"run/2"}` || got[i].timestamp != strconv.FormatInt(t0.Add(offset).Unix(), 10) {
			t.Fatalf("request %d = %+v", i, got[i])
		}
	}
}

func TestRunEventDeliveryHonorsRetryAfterAndFailsAfterTheWindow(t *testing.T) {
	h := newDeliveryHarness(t, webhook.Policy{Local: true})
	rx := newReceiver(t, 429, 500)
	rx.header = http.Header{"Retry-After": {"120"}}
	t0 := h.clock()
	h.subscribe("sub_a", rx.srv.URL, deliverySecret, t0)
	h.step()
	if r := h.row("sub_a"); !r.NextAt.Equal(t0.Add(2 * time.Minute)) {
		t.Fatalf("Retry-After ignored: %+v", r)
	}
	rx.mu.Lock()
	rx.header = nil
	rx.mu.Unlock()
	for range 100 {
		r := h.row("sub_a")
		if r.State != "pending" {
			break
		}
		h.advanceTo(r.NextAt)
		h.step()
	}
	r := h.row("sub_a")
	if r.State != "failed" || r.LastStatus.Int64 != 500 || r.LastError != "http_status" || !r.Finished {
		t.Fatalf("dead letter %+v", r)
	}
	if h.clock().After(t0.Add(store.RunEventDeliveryWindow)) || r.Attempts < 25 || r.Attempts > 32 || int(r.Attempts) != len(rx.requests()) {
		t.Fatalf("failed at %v after %d attempts (%d requests)", h.clock().Sub(t0), r.Attempts, len(rx.requests()))
	}
	h.advanceTo(t0.Add(48 * time.Hour))
	h.step()
	if n := len(rx.requests()); int64(n) != r.Attempts {
		t.Fatalf("failed delivery was sent again: %d requests", n)
	}
}

func TestRunEventDeliveryRecordsSafeCodesWithoutSending(t *testing.T) {
	rx := newReceiver(t, 200)
	local := newDeliveryHarness(t, webhook.Policy{Local: true})
	local.subscribe("sub_bad", rx.srv.URL, "not-a-whsec-secret", local.clock())
	local.step()
	if r := local.row("sub_bad"); r.State != "pending" || r.LastError != "secret" || r.LastStatus.Valid {
		t.Fatalf("unopenable secret %+v", r)
	}
	remote := newDeliveryHarness(t, webhook.Policy{})
	remote.subscribe("sub_loopback", rx.srv.URL, deliverySecret, remote.clock())
	remote.step()
	if r := remote.row("sub_loopback"); r.State != "pending" || r.LastError != "policy" {
		t.Fatalf("controller policy %+v", r)
	}
	if n := len(rx.requests()); n != 0 {
		t.Fatalf("receiver reached %d times", n)
	}
}

func TestSlowReceiverHoldsOneSlotWhileOthersDeliver(t *testing.T) {
	h := newDeliveryHarness(t, webhook.Policy{Local: true})
	release, entered := make(chan struct{}), make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer slow.Close()
	fastHit := make(chan struct{})
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(fastHit)
		w.WriteHeader(http.StatusOK)
	}))
	defer fast.Close()
	t0 := h.clock()
	h.subscribe("sub_slow", slow.URL, deliverySecret, t0)
	h.subscribe("sub_fast", fast.URL, deliverySecret, t0.Add(time.Second))
	if err := h.d.tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-entered
	h.advanceTo(t0.Add(time.Second))
	if err := h.d.tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-fastHit
	close(release)
	h.d.inflight.Wait()
	if h.row("sub_fast").State != "succeeded" || h.row("sub_slow").State != "succeeded" {
		t.Fatalf("fast %+v slow %+v", h.row("sub_fast"), h.row("sub_slow"))
	}
}

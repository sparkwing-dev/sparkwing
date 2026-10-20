package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

const egressLogPath = "/api/v1/runs/run/jobs/job/logs"

type egressLogObjects struct {
	blob.ObjectBackend
	reads int
	bytes map[string][]byte
}

func (o *egressLogObjects) Get(_ context.Context, key string) (io.ReadCloser, error) {
	o.reads++
	body, ok := o.bytes[key]
	if !ok {
		return nil, blob.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

type egressFixture struct {
	store         *store.Store
	objects       *egressLogObjects
	handler       http.Handler
	meter         *egress.Meter
	claim, member string
}

func newEgressFixture(t *testing.T, cfg egress.Config) egressFixture {
	t.Helper()
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	fixture := &serverFixture{}
	e, _, root := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim}, fixture.option(deploymenttest.Team(t), func(s *Server) { s.WithSecretsCipher(cipher) }))
	objects := &egressLogObjects{bytes: map[string][]byte{}}
	meter := egress.New(cfg)
	s := fixture.Server.WithEngineController(e.EngineController).WithEgressMeter(meter).WithDataController(NewDataController(e.Store, DataController{Objects: objects}))
	h := s.Handler()
	response := engineClaimHTTP(t, h, root, "POST", "/api/v1/runners/claim", `{"runner_version":"v1","kinds":["node"],"available_cores":1}`, 200)
	var award controllerwire.ClaimResponse
	if err := json.Unmarshal(response.Body.Bytes(), &award); err != nil || award.Claim == nil {
		t.Fatalf("claim: %s %v", response.Body, err)
	}
	raw := award.Claim.Capability
	engineClaimHTTP(t, h, raw, "POST", "/api/v1/runs/run/jobs/job/transition", `{"to":"running","cause":"start","source_attempt":0}`, 200)
	tenant, err := e.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	f := newScriptedSegmentLogs()
	subject := jobLogSubject(1, award.Claim.Binding.Generation)
	f.add(t, subject, "hello\n", true)
	objects.bytes = f.objects
	principal, err := e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(raw)))
	if err != nil {
		t.Fatal(err)
	}
	actor := engineActor(capability.Authorized{Principal: principal, Binding: principal.Capability.Binding})
	if err := tenant.RecordSegments(t.Context(), store.SegmentCommit{Subject: subject, Actor: actor, Segments: f.views[logSubjectID(subject)].Segments, Final: true, RetentionClass: "r30"}); err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('reader','reader@example.test',1,1)`)
	engineSQL(t, e.Store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','reader','reader',1)`)
	member, _, _, err := e.Store.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: "reader"}, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return egressFixture{handler: h, meter: meter, claim: raw, member: member, store: e.Store, objects: objects}
}

func (f egressFixture) request(method, path, credential string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	if credential != "" {
		kind := "Bearer "
		if credential == f.member {
			kind = "Session "
		}
		r.Header.Set("Authorization", kind+credential)
	}
	return r
}

func (f egressFixture) fetch(method, path, credential string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, f.request(method, path, credential))
	return w
}

func TestMeteredRoutesStillServeTheRunnerAndCLITokens(t *testing.T) {
	f := newEgressFixture(t, egress.Config{})
	for _, credential := range []string{f.claim, f.member} {
		w := f.fetch("GET", egressLogPath, credential)
		if w.Code != 200 || w.Body.String() != "hello\n" {
			t.Fatalf("log read: %d %s", w.Code, w.Body)
		}
	}
	state := f.meter.State()
	if state.GlobalMonthBytes != 12 || len(state.Top) != 2 {
		t.Fatalf("separate readers: %+v", state)
	}
}

func TestDownloadRoutesRefuseARequestWithoutABearer(t *testing.T) {
	f := newEgressFixture(t, egress.Config{})
	for _, path := range []string{egressLogPath, egressLogPath + "/stream"} {
		w := f.fetch("GET", path, "")
		if w.Code != 401 {
			t.Fatalf("anonymous read: %d %s", w.Code, w.Body)
		}
	}
	if state := f.meter.State(); state.GlobalMonthBytes != 0 {
		t.Fatalf("refusal charged: %+v", state)
	}
}

func TestHeadRequestsChargeNothingAndHoldNoSlot(t *testing.T) {
	f := newEgressFixture(t, egress.Config{GlobalDailyAlarmBytes: 4, MaxDownloadsPerPrincipal: 1})
	release, err := f.meter.Open("a/reader", egress.SlotDownload)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for range 8 {
		w := f.fetch("HEAD", egressLogPath, f.member)
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("HEAD: %d %s", w.Code, w.Body)
		}
	}
	if state := f.meter.State(); state.GlobalDayBytes != 0 || state.Alarm {
		t.Fatalf("HEAD charged: %+v", state)
	}
}

func TestConcurrentDownloadsAreCappedPerPrincipal(t *testing.T) {
	f := newEgressFixture(t, egress.Config{MaxDownloadsPerPrincipal: 1})
	release, err := f.meter.Open("a/reader", egress.SlotDownload)
	if err != nil {
		t.Fatal(err)
	}
	assertEgressLimit(t, f.fetch("GET", egressLogPath, f.member), "egress_download_limit")
	release()
	for range 2 {
		w := f.fetch("GET", egressLogPath, f.member)
		if w.Code != 200 || w.Body.String() != "hello\n" {
			t.Fatalf("released slot: %d %s", w.Code, w.Body)
		}
	}
}

func assertEgressLimit(t *testing.T, w *httptest.ResponseRecorder, reason string) {
	t.Helper()
	var body apierr.Envelope
	if w.Code != 429 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Error == nil || body.Error.Code != apierr.RateLimited || body.Error.Details["reason"] != reason || w.Header().Get("Retry-After") == "" {
		t.Fatalf("limit: %d %s", w.Code, w.Body)
	}
}

type egressFlushRecorder struct {
	*httptest.ResponseRecorder
	onFlush func()
}

func (w *egressFlushRecorder) Flush() {
	w.ResponseRecorder.Flush()
	if run := w.onFlush; run != nil {
		w.onFlush = nil
		run()
	}
}

func TestLiveLogStreamCapRefusesPastTheLimit(t *testing.T) {
	f := newEgressFixture(t, egress.Config{MaxStreamsPerPrincipal: 1})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	checked := false
	w := &egressFlushRecorder{ResponseRecorder: httptest.NewRecorder(), onFlush: func() {
		checked = true
		assertEgressLimit(t, f.fetch("GET", egressLogPath+"/stream", f.member), "egress_stream_limit")
		cancel()
	}}
	f.handler.ServeHTTP(w, f.request("GET", egressLogPath+"/stream", f.member).WithContext(ctx))
	if !checked || w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("stream: checked=%v %d %s", checked, w.Code, w.Body)
	}
}

func TestHealthReportsTheAlarmWithoutUsage(t *testing.T) {
	f := newEgressFixture(t, egress.Config{GlobalDailyAlarmBytes: 6})
	for _, alarm := range []bool{false, true} {
		if alarm {
			w := f.fetch("GET", egressLogPath, f.member)
			if w.Code != 200 {
				t.Fatalf("read: %d %s", w.Code, w.Body)
			}
		}
		w := f.fetch("GET", "/api/v1/health", "")
		var health struct {
			Status   string         `json:"status"`
			Problems []string       `json:"problems"`
			Egress   map[string]any `json:"egress"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &health) != nil || len(health.Egress) != 2 || health.Egress["enabled"] != true || health.Egress["alarm"] != alarm {
			t.Fatalf("health: %d %s", w.Code, w.Body)
		}
		if alarm && (health.Status != "degraded" || len(health.Problems) != 1 || health.Problems[0] != "egress: daily alarm threshold reached") {
			t.Fatalf("alarm: %s", w.Body)
		}
	}
}

func TestARequestNamingAnotherPodSharesThePrincipalsDownloadSlot(t *testing.T) {
	f := newEgressFixture(t, egress.Config{MaxDownloadsPerPrincipal: 1})
	release, err := f.meter.Open("a/reader", egress.SlotDownload)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	property := func(noise []byte) bool {
		r := f.request("GET", egressLogPath, f.member)
		value := strings.Repeat("x", len(noise)+1)
		for _, header := range []string{store.RunnerIdentityHeader, "X-Sparkwing-Claim-Holder", "X-Sparkwing-Team"} {
			r.Header.Set(header, value)
		}
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		return w.Code == 429 && !strings.Contains(w.Body.String(), value)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}

func TestDashboardLogStreamSharesTheLiveLogStreamCap(t *testing.T) {
	f := newEgressFixture(t, egress.Config{MaxStreamsPerPrincipal: 1})
	release, err := f.meter.Open("a/reader", egress.SlotLogStream)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	assertEgressLimit(t, f.fetch("GET", egressLogPath+"/stream", f.member), "egress_stream_limit")
	for _, old := range []string{"/api/v1/runs/run/nodes/job/logs/stream", "/api/v1/runs/run/logs/job/stream"} {
		w := f.fetch("GET", old, f.member)
		if w.Code != 404 {
			t.Fatalf("retired stream: %d %s", w.Code, w.Body)
		}
	}
}

func TestNativeLogReadAuthorityProperty(t *testing.T) {
	f := newEgressFixture(t, egress.Config{})
	property := func(choice uint8, seed uint32) bool {
		path, raw := egressLogPath, f.claim
		switch choice % 6 {
		case 0:
			path = fmt.Sprintf("/api/v1/runs/run/jobs/foreign-%d/logs", seed)
		case 1:
			path = fmt.Sprintf("/api/v1/runs/foreign-%d/jobs/job/logs", seed)
		case 2:
			engineSQL(t, f.store, `UPDATE nodes SET generation=generation+1 WHERE team='a' AND run_id='run' AND node_id='job'`)
		case 3:
			engineSQL(t, f.store, `UPDATE nodes SET claimed_by='foreign' WHERE team='a' AND run_id='run' AND node_id='job'`)
		case 4:
			raw = f.member
			engineSQL(t, f.store, `DELETE FROM memberships WHERE team='a' AND account_id='reader'`)
		case 5:
			raw = fmt.Sprintf("swk_unknown_%d", seed)
		}
		before := f.objects.reads
		w := f.fetch("GET", path, raw)
		switch choice % 6 {
		case 2:
			engineSQL(t, f.store, `UPDATE nodes SET generation=generation-1 WHERE team='a' AND run_id='run' AND node_id='job'`)
		case 3:
			engineSQL(t, f.store, `UPDATE nodes SET claimed_by='root' WHERE team='a' AND run_id='run' AND node_id='job'`)
		case 4:
			engineSQL(t, f.store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','reader','reader',1)`)
		}
		if w.Code != 401 && w.Code != 403 && w.Code != 404 {
			t.Logf("refusal: %d %s", w.Code, w.Body)
			return false
		}
		return !strings.Contains(w.Body.String(), "hello") && f.objects.reads == before && f.meter.State().GlobalMonthBytes == 0
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 36}); err != nil {
		t.Fatal(err)
	}
	if w := f.fetch("GET", egressLogPath, f.claim); w.Code != 200 || w.Body.String() != "hello\n" {
		t.Fatalf("valid claim: %d %s", w.Code, w.Body)
	}
}

func TestNativeLogReadEndsWithItsRoot(t *testing.T) {
	f := newEgressFixture(t, egress.Config{})
	revokeEngineMachine(t, f.store, "root")
	before := f.objects.reads
	w := f.fetch("GET", egressLogPath, f.claim)
	if w.Code != 401 || strings.Contains(w.Body.String(), "hello") || f.objects.reads != before || f.meter.State().GlobalMonthBytes != 0 {
		t.Fatalf("revoked read: %d %s", w.Code, w.Body)
	}
}

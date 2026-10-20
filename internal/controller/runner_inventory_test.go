package controller

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type runnerInventoryFake struct {
	team     store.Team
	snapshot capability.RunnerSnapshot
	err      error
	calls    int
}

func (f *runnerInventoryFake) Team() store.Team { return f.team }
func (f *runnerInventoryFake) ReadRunners(context.Context) (capability.RunnerSnapshot, error) {
	f.calls++
	return f.snapshot, f.err
}

func runnerInventoryFixture(t *testing.T, f *runnerInventoryFake) (http.Handler, string) {
	t.Helper()
	s, session := credentialSessionServer(t)
	testOnlyTenantPort[RunnerInventoryReader](s, func(_ context.Context, team store.Team) (RunnerInventoryReader, error) {
		if team != "a" {
			t.Fatalf("selected team %q", team)
		}
		return f, nil
	})
	h := s.Handler()
	w := credentialRequestHTTP(t, h, session, `{"grants":["runs.read"]}`, false)
	var issued credentialResponse
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &issued) != nil {
		t.Fatalf("member issuance: %d %s", w.Code, w.Body.String())
	}
	return h, issued.Capability
}

func TestRunnerInventoryHTTPUsesCommittedSnapshot(t *testing.T) {
	now := time.Unix(1800000000, 123).UTC()
	f := &runnerInventoryFake{team: "a", snapshot: capability.RunnerSnapshot{Now: now, Runners: []capability.RunnerObservation{
		{
			Team: "a", Runner: nodeproto.RunnerIdentity{Name: "same", Type: "runner", Labels: []string{"gpu"}}, Location: "machine", LastSeen: now, ActiveJobs: []string{"run/job", "run/job"}, MaxConcurrent: 2,
			Headroom: &capability.RunnerHeadroom{Cores: 1.5, MemoryBytes: 1024, QueueDepth: 3, ObservedAt: now},
		},
		{Team: "a", Runner: nodeproto.RunnerIdentity{Name: "same", Type: "runner"}, Location: "machine", LastSeen: now.Add(-3 * time.Minute)},
		{
			Team: "a", Runner: nodeproto.RunnerIdentity{Name: "idle", Type: "runner"}, Location: "machine", LastSeen: now,
			Headroom: &capability.RunnerHeadroom{Cores: 1, ObservedAt: now.Add(-2 * time.Hour)},
		},
	}}}
	h, raw := runnerInventoryFixture(t, f)
	w := engineClaimHTTP(t, h, raw, "GET", "/api/v1/runners?team=foreign", "", 200)
	var response struct {
		Runners []controllerwire.RunnerDetail `json:"runners"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Runners) != 3 || f.calls != 1 {
		t.Fatalf("snapshot: %+v, calls=%d", response, f.calls)
	}
	busy, offline, idle := response.Runners[0], response.Runners[1], response.Runners[2]
	if busy.Name != "same" || busy.Status != "busy" || busy.Class != "runner" || busy.Location != "machine" || busy.MaxConcurrent != 2 || !reflect.DeepEqual(busy.ActiveJobs, []controllerwire.RunnerJob{{RunID: "run", JobID: "job"}}) || !reflect.DeepEqual(busy.Labels, []string{"gpu"}) || busy.Headroom == nil || busy.Headroom.Cores != 1.5 || busy.Headroom.ObservedAt != now.Format(time.RFC3339Nano) {
		t.Fatalf("busy runner: %+v", busy)
	}
	if offline.Status != "offline" || offline.Headroom != nil || idle.Status != "idle" || idle.Headroom != nil {
		t.Fatalf("liveness: %+v", response.Runners)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("runner inventory may be cached")
	}
	if len(f.snapshot.Runners[0].ActiveJobs) != 2 {
		t.Fatal("projection changed the stored snapshot")
	}
	engineClaimHTTP(t, h, "", "GET", "/api/v1/runners", "", 401)
	if f.calls != 1 {
		t.Fatal("anonymous request read inventory")
	}
}

func TestRunnerInventoryHTTPTenantProperty(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	f := &runnerInventoryFake{team: "a"}
	h, raw := runnerInventoryFixture(t, f)
	property := func(team, name string, first bool) bool {
		if team == "" || team == "a" {
			team = "foreign" + team
		}
		foreign := capability.RunnerObservation{Team: team, Runner: nodeproto.RunnerIdentity{Name: name, Type: "runner"}, Location: "machine", LastSeen: now}
		own := capability.RunnerObservation{Team: "a", Runner: nodeproto.RunnerIdentity{Name: "allowed", Type: "runner"}, Location: "machine", LastSeen: now}
		f.snapshot = capability.RunnerSnapshot{Now: now, Runners: []capability.RunnerObservation{own, foreign}}
		if first {
			f.snapshot.Runners[0], f.snapshot.Runners[1] = foreign, own
		}
		w := engineClaimHTTP(t, h, raw, "GET", "/api/v1/runners", "", 500)
		return !strings.Contains(w.Body.String(), `"runners"`) && !strings.Contains(w.Body.String(), `"allowed"`)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 100}); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerInventoryHTTPNameProperty(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	f := &runnerInventoryFake{team: "a"}
	h, raw := runnerInventoryFixture(t, f)
	if err := quick.Check(func(name string) bool {
		if name == "" {
			name = "runner"
		}
		f.snapshot = capability.RunnerSnapshot{Now: now, Runners: []capability.RunnerObservation{
			{Team: "a", Runner: nodeproto.RunnerIdentity{Name: name, Type: "runner"}, LastSeen: now, ActiveJobs: []string{"own/job"}},
			{Team: "a", Runner: nodeproto.RunnerIdentity{Name: name, Type: "runner"}, LastSeen: now.Add(-time.Hour), ActiveJobs: []string{}},
		}}
		w := engineClaimHTTP(t, h, raw, "GET", "/api/v1/runners", "", 200)
		var response struct {
			Runners []controllerwire.RunnerDetail `json:"runners"`
		}
		if json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Runners) != 2 {
			return false
		}
		return response.Runners[0].Name == name && response.Runners[1].Name == name &&
			response.Runners[0].Status == "busy" && response.Runners[1].Status == "offline" &&
			len(response.Runners[0].ActiveJobs) == 1 && len(response.Runners[1].ActiveJobs) == 0
	}, &quick.Config{MaxCount: 100}); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerInventoryHTTPRequiresMemberCredential(t *testing.T) {
	s, session := credentialSessionServer(t)
	f := &runnerInventoryFake{team: "a", snapshot: capability.RunnerSnapshot{Now: time.Now()}}
	testOnlyTenantPort[RunnerInventoryReader](s, func(context.Context, store.Team) (RunnerInventoryReader, error) { return f, nil })
	h := s.Handler()
	issued := credentialRequestHTTP(t, h, session, `{"grants":["runs.read"]}`, false)
	var member credentialResponse
	if issued.Code != 201 || json.Unmarshal(issued.Body.Bytes(), &member) != nil {
		t.Fatalf("member issuance: %d %s", issued.Code, issued.Body.String())
	}
	e := &EngineController{Store: s.store}
	machine := engineHTTPRoot(t, e, "a", "machine", []capability.Grant{capability.RunsRead})
	operator := engineHTTPRoot(t, e, "", "operator", []capability.Grant{capability.OperatorTokens, capability.RunsRead})
	for _, raw := range []string{machine, operator} {
		engineClaimHTTP(t, h, raw, "GET", "/api/v1/runners", "", 403)
	}
	engineClaimHTTP(t, h, member.Capability, "GET", "/api/v1/runners", "", 200)
	engineSQL(t, s.store, `DELETE FROM memberships WHERE team='a' AND account_id='member'`)
	engineClaimHTTP(t, h, member.Capability, "GET", "/api/v1/runners", "", 401)
	if f.calls != 1 {
		t.Fatalf("refused authority read runner inventory: %d calls", f.calls)
	}
}

func TestRunnerInventoryHTTPRefusesMissingAndCorruptFacts(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	f := &runnerInventoryFake{team: "a", snapshot: capability.RunnerSnapshot{Now: now}}
	h, raw := runnerInventoryFixture(t, f)
	w := engineClaimHTTP(t, h, raw, "GET", "/api/v1/runners", "", 200)
	if strings.TrimSpace(w.Body.String()) != `{"runners":[]}` {
		t.Fatalf("empty inventory: %s", w.Body.String())
	}
	for _, mutate := range []func(){
		func() { f.team = "foreign" },
		func() { f.err = errors.New("private inventory credential") },
		func() { f.snapshot.Now = time.Time{} },
		func() { f.snapshot.Runners = []capability.RunnerObservation{{Team: "a"}} },
		func() {
			f.snapshot.Runners = []capability.RunnerObservation{{Team: "a", Runner: nodeproto.RunnerIdentity{Name: "bad", Type: "runner"}, MaxConcurrent: -1}}
		},
		func() {
			f.snapshot.Runners = []capability.RunnerObservation{{Team: "a", Runner: nodeproto.RunnerIdentity{Name: "bad", Type: "runner"}, Headroom: &capability.RunnerHeadroom{Cores: math.NaN(), ObservedAt: now}}}
		},
	} {
		f.team, f.err, f.snapshot = "a", nil, capability.RunnerSnapshot{Now: now}
		mutate()
		w := engineClaimHTTP(t, h, raw, "GET", "/api/v1/runners", "", 500)
		if strings.Contains(w.Body.String(), "private inventory") || strings.Contains(w.Body.String(), `"runners"`) {
			t.Fatalf("unsafe failure: %s", w.Body.String())
		}
	}
	s, session := credentialSessionServer(t)
	h = s.Handler()
	issued := credentialRequestHTTP(t, h, session, `{"grants":["runs.read"]}`, false)
	var credential credentialResponse
	if issued.Code != 201 || json.Unmarshal(issued.Body.Bytes(), &credential) != nil {
		t.Fatalf("member issuance: %d %s", issued.Code, issued.Body.String())
	}
	testOnlyTenantPort[RunnerInventoryReader](s, func(context.Context, store.Team) (RunnerInventoryReader, error) { return nil, nil })
	h = s.Handler()
	w = engineClaimHTTP(t, h, credential.Capability, "GET", "/api/v1/runners", "", 500)
	if !strings.Contains(w.Body.String(), `"code":"policy_missing"`) {
		t.Fatalf("missing adapter: %s", w.Body.String())
	}
}

func TestRunnerInventoryHTTPUsesStoreSnapshot(t *testing.T) {
	s, session := credentialSessionServer(t)
	e := &EngineController{Store: s.store}
	raw := engineHTTPRoot(t, e, "a", "observed", []capability.Grant{capability.NodesClaim})
	p, err := s.store.LookupCapability(t.Context(), sha256.Sum256([]byte(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.RecordRunnerPoll(t.Context(), store.UnitDemand{CapabilityPrefix: p.Capability.Prefix, Labels: []string{"gpu", "name=forged"}}); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	issued := credentialRequestHTTP(t, h, session, `{"grants":["runs.read"]}`, false)
	var credential credentialResponse
	if issued.Code != 201 || json.Unmarshal(issued.Body.Bytes(), &credential) != nil {
		t.Fatalf("member issuance: %d %s", issued.Code, issued.Body.String())
	}
	w := engineClaimHTTP(t, h, credential.Capability, "GET", "/api/v1/runners?team=b", "", 200)
	var response struct {
		Runners []controllerwire.RunnerDetail `json:"runners"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Runners) != 1 || response.Runners[0].Name != "observed" || response.Runners[0].Class != "runner" || response.Runners[0].Status != "idle" || !reflect.DeepEqual(response.Runners[0].Labels, []string{"gpu"}) {
		t.Fatalf("store inventory: %+v", response)
	}
	reader, err := client.NewRunnerReader(client.EngineOptions{URL: "https://controller", Bearer: func(context.Context) (string, error) { return credential.Capability, nil }, HTTP: &http.Client{Transport: inventoryRoundTrip{h}}})
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := reader.ListRunners(t.Context())
	if err != nil || len(inventory.Runners) != 1 || inventory.Runners[0].Name != "observed" {
		t.Fatalf("store-to-client inventory: %+v %v", inventory, err)
	}
}

func recordMetricRunner(t *testing.T, st *store.Store, team store.Team, name string, labels []string) {
	t.Helper()
	raw := engineHTTPRoot(t, &EngineController{Store: st}, string(team), name, []capability.Grant{capability.NodesClaim})
	principal, err := st.LookupCapability(t.Context(), sha256.Sum256([]byte(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordRunnerPoll(t.Context(), store.UnitDemand{CapabilityPrefix: principal.Capability.Prefix, Labels: labels}); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerInventoryMetricsPreservesSamplesOnReadFailure(t *testing.T) {
	restoreGlobals(t)
	t.Cleanup(func() { setActiveRunners(0) })
	s, _ := credentialSessionServer(t)
	for _, name := range []string{"one", "two"} {
		recordMetricRunner(t, s.engineController.Store, "a", name, []string{"gpu"})
	}
	s.sampleRunners(t.Context())
	if got := mustSample(t, scrapeRegistry(t), "sparkwing_active_runners"); got != 2 {
		t.Fatalf("distinct live credentials = %v, want 2", got)
	}
	if got := mustSample(t, scrapeRegistry(t), `sparkwing_runners_live{label_set="gpu"}`); got != 2 {
		t.Fatalf("observed runner labels = %v, want 2", got)
	}
	engineSQL(t, s.engineController.Store, `DROP TABLE capabilities`)
	s.sampleRunners(t.Context())
	if got := mustSample(t, scrapeRegistry(t), "sparkwing_active_runners"); got != 2 {
		t.Fatalf("failed snapshot replaced the last sample: %v", got)
	}
	if got := mustSample(t, scrapeRegistry(t), `sparkwing_runners_live{label_set="gpu"}`); got != 2 {
		t.Fatalf("failed snapshot replaced label series: %v", got)
	}
}

func TestRunnerInventoryRealClient(t *testing.T) {
	now := time.Now().UTC()
	f := &runnerInventoryFake{team: "a", snapshot: capability.RunnerSnapshot{Now: now, Runners: []capability.RunnerObservation{{Team: "a", Runner: nodeproto.RunnerIdentity{Name: "box", Type: "runner", Labels: []string{"gpu"}}, Location: "machine", LastSeen: now, ActiveJobs: []string{"run/build/linux"}}}}}
	h, raw := runnerInventoryFixture(t, f)
	reader, err := client.NewRunnerReader(client.EngineOptions{URL: "https://controller", Bearer: func(context.Context) (string, error) { return raw, nil }, HTTP: &http.Client{Transport: inventoryRoundTrip{h}}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := reader.ListRunners(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Runners) != 1 || got.Runners[0].Name != "box" || got.Runners[0].Class != "runner" || !reflect.DeepEqual(got.Runners[0].Labels, []string{"gpu"}) || !reflect.DeepEqual(got.Runners[0].ActiveJobs, []controllerwire.RunnerJob{{RunID: "run", JobID: "build/linux"}}) {
		t.Fatalf("inventory: %+v", got)
	}
}

type inventoryRoundTrip struct{ handler http.Handler }

func (tr inventoryRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	tr.handler.ServeHTTP(w, r)
	return w.Result(), nil
}

func TestProfileIdentityProbeAcceptsDeploymentAndTeamCredentials(t *testing.T) {
	s, session := credentialSessionServer(t)
	e := &EngineController{Store: s.store}
	deployment := engineHTTPRoot(t, e, "", "deployment", []capability.Grant{capability.OperatorTokens})
	machine := engineHTTPRoot(t, e, "a", "machine", []capability.Grant{capability.NodesClaim})
	h := s.Handler()
	w := credentialRequestHTTP(t, h, session, `{"grants":["runs.read"]}`, false)
	var member credentialResponse
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &member) != nil {
		t.Fatalf("issuance: %d %s", w.Code, w.Body)
	}
	for _, raw := range []string{deployment, machine, member.Capability} {
		if err := client.CheckIdentity(t.Context(), client.EngineOptions{URL: "https://controller", Bearer: func(context.Context) (string, error) { return raw, nil }, HTTP: &http.Client{Transport: inventoryRoundTrip{h}}}); err != nil {
			t.Fatal(err)
		}
	}
	engineClaimHTTP(t, h, deployment, "GET", "/api/v1/runs", "", 403)
}

package fixture

import (
	"encoding/json"
	"sparkwing-pipelines/isolationprobe"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwingtest"
)

var registerOnce sync.Once

type planDocument struct {
	Jobs []struct {
		ID        string
		Needs     []string `json:"deps"`
		TimeoutMS int64    `json:"timeout_ms"`
	}
}

func planFixture(t *testing.T, args map[string]string) (planDocument, error) {
	t.Helper()
	registerOnce.Do(Register)
	host := sparkwingtest.New(sparkwingtest.Options{})
	t.Cleanup(func() { _ = host.Close() })
	doc, err := host.Plan(t.Context(), Entrypoint, args)
	if err != nil {
		return planDocument{}, err
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var out planDocument
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out, nil
}

// The probe job depends on the job whose output it may read and runs beside the sibling whose
// output it must not read, and its timeout bounds an anchor's hold.
func TestFixturePlansTheProbeBesideItsSibling(t *testing.T) {
	doc, err := planFixture(t, map[string]string{"role": "anchor", "hold": "15m"})
	if err != nil {
		t.Fatal(err)
	}
	needs := map[string]string{}
	for _, j := range doc.Jobs {
		needs[j.ID] = strings.Join(j.Needs, ",")
		if j.ID == isolationprobe.ProbeJob && time.Duration(j.TimeoutMS)*time.Millisecond != JobTimeout {
			t.Errorf("probe timeout = %dms, want the %s default", j.TimeoutMS, JobTimeout)
		}
	}
	want := map[string]string{isolationprobe.SiblingJob: "", AllowedJob: "", isolationprobe.ProbeJob: AllowedJob}
	if len(needs) != len(want) {
		t.Fatalf("jobs = %+v, want the sibling, the allowed producer and the probe", doc.Jobs)
	}
	for id, deps := range want {
		if got, ok := needs[id]; !ok || got != deps {
			t.Errorf("%s needs %q, want %q", id, got, deps)
		}
	}
}

// A run that raises the probe job's limit gets that limit, and its anchor may hold until just before
// it.
func TestFixtureTakesARaisedTimeLimit(t *testing.T) {
	doc, err := planFixture(t, map[string]string{"role": "anchor", "hold": "33m", "timeout": "35m"})
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range doc.Jobs {
		if j.ID == isolationprobe.ProbeJob && time.Duration(j.TimeoutMS)*time.Millisecond != 35*time.Minute {
			t.Fatalf("probe timeout = %dms, want 35m", j.TimeoutMS)
		}
	}
}

// Discovery plans every registered pipeline with no arguments, so an empty role and hold must plan
// a probe that does not hold.
func TestFixturePlansDiscoverysEmptyArguments(t *testing.T) {
	if _, err := planFixture(t, nil); err != nil {
		t.Fatal(err)
	}
}

func TestFixtureRefusesArgumentsItCannotHonor(t *testing.T) {
	for name, args := range map[string]map[string]string{
		"role":                   {"role": "attacker"},
		"hold":                   {"role": "anchor", "hold": "soon"},
		"hold past limit":        {"role": "anchor", "hold": "20m"},
		"negative hold":          {"role": "anchor", "hold": "-1m"},
		"limit past cap":         {"timeout": "61m"},
		"zero limit":             {"timeout": "0s"},
		"limit by name":          {"timeout": "soon"},
		"hold past limit raised": {"role": "anchor", "hold": "30m", "timeout": "25m"},
	} {
		if _, err := planFixture(t, args); err == nil {
			t.Errorf("%s: plan accepted %v", name, args)
		}
	}
}

func TestSDKOutputProbePassesOnlyTheReadSetRefusal(t *testing.T) {
	refused := sdkOutputProbe("read", func() {
		panic("resolve output-sibling: output is outside the evaluated job read set")
	})
	if refused.Verdict != isolationprobe.Pass {
		t.Fatalf("read-set refusal = %+v, want pass", refused)
	}
	store := sdkOutputProbe("read", func() {
		panic(`sparkwing: Ref[fixture.siblingOut].Get failed (pipeline="isolation-probe" node="output-sibling"): resource is outside the authorized binding`)
	})
	if store.Verdict != isolationprobe.Pass || store.Outcome != "refused: resource is outside the authorized binding" {
		t.Fatalf("store binding refusal = %+v, want pass", store)
	}
	wrapped := sdkOutputProbe("read", func() {
		panic("sparkwing: Ref[fixture.siblingOut].Get: output is outside the evaluated job read set (reason: binding)")
	})
	if wrapped.Verdict != isolationprobe.Pass {
		t.Fatalf("wrapped read-set refusal = %+v, want pass", wrapped)
	}
	dependency := sdkOutputProbe("read", func() {
		panic(`sparkwing: Ref[fixture.siblingOut].Get: job "probe" does not depend on job "output-sibling", so it cannot read its output; ` +
			`add .Needs() on "output-sibling" to job "probe" (a Ref captured by a closure adds no dependency)`)
	})
	if dependency.Verdict != isolationprobe.Pass || strings.Contains(dependency.Outcome, `"`) || strings.Contains(dependency.Outcome, "output-sibling") {
		t.Fatalf("dependency refusal = %+v, want a pass that names no job", dependency)
	}
	other := sdkOutputProbe("read", func() { panic("controller unavailable: VALUE_FROM_THE_HOST") })
	if other.Verdict != isolationprobe.Inconclusive || other.Outcome != "failed without a binding refusal" {
		t.Fatalf("other failure = %+v, want inconclusive with a fixed category", other)
	}
	read := sdkOutputProbe("read", func() {})
	if read.Verdict != isolationprobe.Breach || strings.Contains(read.Outcome, "isolation-probe") {
		t.Fatalf("successful read = %+v, want a breach that carries no value", read)
	}
}

// The allowed read is the control for the refusals: it passes only on the value its producer
// wrote, and neither a value nor a failure's text reaches the result.
func TestAllowedReadProbeIsTheReadPathsControl(t *testing.T) {
	if r := allowedReadProbe(func() string { return allowedMarker }); r.Verdict != isolationprobe.Pass || r.Probe != ProbeSDKAllowed {
		t.Fatalf("allowed read = %+v, want pass", r)
	}
	wrong := allowedReadProbe(func() string { return "VALUE_READ" })
	failed := allowedReadProbe(func() string { panic("host gone: VALUE_FROM_THE_HOST") })
	for _, r := range []isolationprobe.Result{wrong, failed} {
		if r.Verdict != isolationprobe.Inconclusive || strings.Contains(r.Outcome, "VALUE") {
			t.Errorf("result = %+v, want inconclusive without the value", r)
		}
	}
}

// Every SDK probe answers to a key the driver requires, and a read outside a running job fails
// without a binding refusal rather than passing.
func TestSDKProbesReportTheDriversKeys(t *testing.T) {
	results := (&probeJob{}).sdkOutputProbes(t.Context())
	var got []string
	for _, r := range results {
		got = append(got, r.Context+"|"+r.Item+"|"+r.Probe)
		if r.Verdict == isolationprobe.Pass {
			t.Errorf("%s passed outside a Cloud job: %+v", r.Probe, r)
		}
	}
	var want []string
	for _, k := range SDKKeys() {
		want = append(want, k.Context+"|"+k.Item+"|"+k.Probe)
	}
	if strings.Join(got, ";") != strings.Join(want, ";") {
		t.Fatalf("probe keys = %v, want %v", got, want)
	}
}

func TestWorkDirSharesTheSocketsScratchVolume(t *testing.T) {
	if got := workDir("/tmp/docker.sock"); got != "/tmp/isolation-probe" {
		t.Fatalf("workDir = %q, want beside the socket", got)
	}
}

// The run's failure message is printed into the same log the driver parses, so it must not carry
// a marker the driver would read as a result.
func TestProbeFailureMessageCarriesNoMarker(t *testing.T) {
	err := (&probeJob{}).verdict(false)
	for _, marker := range []string{isolationprobe.ResultMarker, isolationprobe.ReadyMarker, isolationprobe.SummaryMarker} {
		if strings.Contains(err.Error(), strings.TrimSpace(marker)) {
			t.Fatalf("failure message %q carries %q", err, marker)
		}
	}
}

func TestDockerSocketTakesOnlyAUnixHost(t *testing.T) {
	for host, want := range map[string]string{
		"unix:///tmp/docker.sock": "/tmp/docker.sock",
		"tcp://127.0.0.1:2375":    "",
		"":                        "",
	} {
		if got := dockerSocket(host); got != want {
			t.Errorf("dockerSocket(%q) = %q, want %q", host, got, want)
		}
	}
}

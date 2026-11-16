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

// The probe job runs beside the sibling whose output it must not read, never after it, and its
// timeout bounds an anchor's hold.
func TestFixturePlansTheProbeBesideItsSibling(t *testing.T) {
	doc, err := planFixture(t, map[string]string{"role": "anchor", "hold": "15m"})
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Jobs) != 2 {
		t.Fatalf("jobs = %+v, want the sibling and the probe", doc.Jobs)
	}
	for _, j := range doc.Jobs {
		if len(j.Needs) != 0 {
			t.Errorf("%s needs %v; the probe must not depend on the sibling", j.ID, j.Needs)
		}
		if j.ID == isolationprobe.ProbeJob && time.Duration(j.TimeoutMS)*time.Millisecond != JobTimeout {
			t.Errorf("probe timeout = %dms, want %s", j.TimeoutMS, JobTimeout)
		}
	}
	if doc.Jobs[0].ID != isolationprobe.SiblingJob || doc.Jobs[1].ID != isolationprobe.ProbeJob {
		t.Fatalf("jobs = %+v", doc.Jobs)
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
		"role":            {"role": "attacker"},
		"hold":            {"role": "anchor", "hold": "soon"},
		"hold past limit": {"role": "anchor", "hold": "20m"},
		"negative hold":   {"role": "anchor", "hold": "-1m"},
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
	other := sdkOutputProbe("read", func() { panic("controller unavailable") })
	if other.Verdict != isolationprobe.Inconclusive {
		t.Fatalf("other failure = %+v, want inconclusive", other)
	}
	read := sdkOutputProbe("read", func() {})
	if read.Verdict != isolationprobe.Breach || strings.Contains(read.Outcome, "isolation-probe") {
		t.Fatalf("successful read = %+v, want a breach that carries no value", read)
	}
}

func TestWorkDirSharesTheSocketsScratchVolume(t *testing.T) {
	if got := workDir("/tmp/docker.sock"); got != "/tmp/isolation-probe" {
		t.Fatalf("workDir = %q, want beside the socket", got)
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

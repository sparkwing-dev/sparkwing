package isolationprobe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validPlan() Plan {
	return Plan{
		Team: "stg-iso-a", ControllerURL: "https://staging.sparkwing.dev", ListenPort: 18080, Canary: "isolation-canary-stg-iso-a",
		PeerCanaries: []string{"isolation-canary-stg-iso-b"}, JobNamespace: "jobs-ns", KubeNamespaces: []string{"jobs-ns", "staging-ns"},
		KubePods: []PodRef{{Namespace: "staging-ns", Name: "controller-0"}},
		Targets: []Target{
			{Item: "C4", Name: "IMDS", Address: "169.254.169.254:80", Expect: ExpectBlocked, Listening: true, Grade: GradeIMDS},
			{Item: "C7", Name: "public kube API", Address: "api.example.invalid:443", Expect: ExpectDenied, Listening: true, Grade: GradeKubeAPI, Namespace: "staging-ns"},
			{Item: "C5", Name: "IMDS IPv6", Address: "[fd00:ec2::254]:80", Expect: ExpectBlocked},
		},
	}
}

func TestReadPlanAcceptsAPlanItCanJudge(t *testing.T) {
	path := filepath.Join(t.TempDir(), PlanFile)
	if err := os.WriteFile(path, []byte(`{"team":"stg-iso-a","controller_url":"https://staging.sparkwing.dev","listen_port":18080,"canary":"c","targets":[{"item":"C6","name":"controller","address":"10.9.3.3:4343","expect":"blocked","listening":true}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := ReadPlan(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Targets) != 1 || !p.Targets[0].Listening || p.Targets[0].Expect != ExpectBlocked {
		t.Fatalf("plan = %+v", p)
	}
	if err := validPlan().Validate(); err != nil {
		t.Fatal(err)
	}
}

// A plan the probe could not judge is refused before any probe runs, so no target passes because
// the probe misread it.
func TestValidateRefusesAPlanItCannotJudge(t *testing.T) {
	for name, c := range map[string]struct {
		edit func(*Plan)
		want string
	}{
		"listen port":        {func(p *Plan) { p.ListenPort = 0 }, "listen_port"},
		"canary path":        {func(p *Plan) { p.Canary = "../etc/passwd" }, "canary"},
		"peer canary path":   {func(p *Plan) { p.PeerCanaries = []string{"a/b"} }, "peer canary"},
		"controller url":     {func(p *Plan) { p.ControllerURL = "staging.sparkwing.dev" }, "controller_url"},
		"address":            {func(p *Plan) { p.Targets[0].Address = "169.254.169.254" }, "address"},
		"expect":             {func(p *Plan) { p.Targets[0].Expect = "maybe" }, "unknown expect"},
		"ungraded denial":    {func(p *Plan) { p.Targets[1].Grade = "" }, "needs a grade"},
		"kube API namespace": {func(p *Plan) { p.Targets[1].Namespace = "" }, "namespace it asks about"},
		"job namespace":      {func(p *Plan) { p.JobNamespace = "" }, "job_namespace"},
		"no kube namespaces": {func(p *Plan) { p.KubeNamespaces = nil }, "kube_namespaces"},
		"kube pod path":      {func(p *Plan) { p.KubePods = []PodRef{{Namespace: "staging-ns", Name: "../x"}} }, "kube pod"},
		"grade":              {func(p *Plan) { p.Targets[0].Grade = "ssh" }, "unknown grade"},
		"unnamed target":     {func(p *Plan) { p.Targets[2].Name = "" }, "item and name"},
		"unclassified probe": {func(p *Plan) { p.Targets[2].Item = "" }, "item and name"},
	} {
		p := validPlan()
		p.Targets = append([]Target(nil), p.Targets...)
		c.edit(&p)
		err := p.Validate()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: Validate() = %v, want an error naming %q", name, err, c.want)
		}
	}
}

func TestParseLogFindsMarkerLinesBehindLogPrefixes(t *testing.T) {
	result := Result{Item: "C4", Context: ContextJob, Probe: "IMDS", Expect: ExpectBlocked, Outcome: outcomeTimeout, Verdict: Pass}
	text := strings.Join([]string{
		"2026-11-16T07:00:00Z probe | " + FormatLine(ReadyMarker, Ready{PodIPs: []string{"10.9.1.2"}, ListenPort: 18080, NetNS: "net:[1]"}),
		"unrelated output",
		"[probe] " + FormatLine(ResultMarker, result),
		FormatLine(SummaryMarker, Summarize([]Result{result})),
	}, "\n")
	log, err := ParseLog(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(log.Ready) != 1 || log.Ready[0].PodIPs[0] != "10.9.1.2" || len(log.Results) != 1 || log.Results[0] != result || len(log.Summaries) != 1 || !log.Summaries[0].OK {
		t.Fatalf("parsed log = %+v", log)
	}
	if _, err := ParseLog("x " + ResultMarker + "{not json"); err == nil {
		t.Fatal("a marker line that does not parse was dropped")
	}
}

func TestSummarizeFailsOnAnyBreachInconclusiveOrNothing(t *testing.T) {
	if Summarize(nil).OK {
		t.Fatal("no results passed")
	}
	ok := Summarize([]Result{{Verdict: Pass}, {Verdict: Observed}, {Verdict: NotProbed}})
	if !ok.OK || ok.Pass != 1 || ok.Observed != 1 || ok.NotProbed != 1 {
		t.Fatalf("summary = %+v", ok)
	}
	for _, v := range []Verdict{Breach, Inconclusive, "unknown"} {
		if s := Summarize([]Result{{Verdict: Pass}, {Verdict: v}}); s.OK {
			t.Errorf("%s passed: %+v", v, s)
		}
	}
}

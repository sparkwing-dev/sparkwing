package match_test

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// safety: the store's label filter before pkg/match replaced it, kept so the
// parity test pins that equality selectors decide exactly as they did.
func legacyLabelsSatisfied(needed, have []string) bool {
	set := map[string]bool{}
	for _, label := range have {
		if label != "" && label != "local" && !strings.HasPrefix(label, "location=") {
			set[label] = true
		}
	}
	for _, term := range needed {
		if term == "" {
			continue
		}
		ok := false
		for _, alt := range strings.Split(term, ",") {
			if alt = strings.TrimSpace(alt); alt != "" && set[alt] {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func TestEvaluateMatchesTheLegacyFilterForEqualitySelectors(t *testing.T) {
	labelSets := [][]string{nil, {"linux"}, {"linux", "gpu"}, {"os=linux", "arch=amd64"}, {"local", "gpu"}, {"location=local"}}
	selectors := [][]string{
		nil,
		{"linux"},
		{"gpu"},
		{"linux,macos", "gpu"},
		{"os=linux"},
		{"os=linux,os=macos", "arch=amd64"},
		{"local"},
		{"location=local"},
		{""},
		{"gpu,local"},
	}
	for _, labels := range labelSets {
		for _, selector := range selectors {
			profile := match.Profile{Class: match.ClassAgent, Labels: match.SelfAsserted(labels)}
			got := match.Evaluate(profile, match.Demand{Selector: selector}).OK()
			if want := legacyLabelsSatisfied(selector, labels); got != want {
				t.Errorf("labels %v selector %v: Evaluate = %v, legacy = %v", labels, selector, got, want)
			}
		}
	}
}

func TestSelfAssertedLabelsCannotClaimAGrantedField(t *testing.T) {
	asserted := match.Profile{Class: match.ClassAgent, Labels: match.SelfAsserted([]string{
		"name=moonborn", "class=coordinator", "team=acme", "local", "location=cloud", "gpu",
	})}
	for _, term := range []string{"name=moonborn", "class=coordinator", "team=acme", "local", "location=cloud"} {
		if v := match.Evaluate(asserted, match.Demand{Selector: []string{term}}); v.OK() {
			t.Errorf("a self-asserted label satisfied %q", term)
		}
	}
	granted := match.Profile{Name: "moonborn", Class: match.ClassAgent}
	if v := match.Evaluate(granted, match.Demand{Selector: []string{"name=moonborn"}}); !v.OK() {
		t.Errorf("the granted name did not satisfy name=moonborn: %v", v)
	}
}

func TestObservedPlatformOverridesAnAssertedOne(t *testing.T) {
	p := match.Profile{OS: "linux", Arch: "arm64", Labels: []string{"arch=amd64"}}
	if match.Evaluate(p, match.Demand{Selector: []string{"arch=amd64"}}).OK() {
		t.Error("an asserted arch=amd64 beat the observed arm64")
	}
	if !match.Evaluate(p, match.Demand{Selector: []string{"os=linux", "arch=arm64"}}).OK() {
		t.Error("the observed platform did not satisfy its own terms")
	}
}

// Every agent takes work that names no selector: selected mode, where an agent
// takes only what selects it, is not offered.
func TestAnAgentTakesUnselectedWork(t *testing.T) {
	p := match.Profile{Name: "pi", Class: match.ClassAgent, Labels: []string{"arm"}}
	if v := match.Evaluate(p, match.Demand{}); !v.OK() {
		t.Fatalf("an agent refused a node with no selector: %v", v)
	}
}

// Eligibility is static: a busy agent is still eligible, so a WhenRunner or
// hold decision built on it never turns on a momentary lack of room.
func TestEvaluateIgnoresAvailability(t *testing.T) {
	p := match.Profile{Capacity: match.Resources{Cores: 4, MemoryBytes: 8 << 30}}
	request := match.Resources{Cores: 2}
	if v := match.Evaluate(p, match.Demand{Request: request}); !v.OK() {
		t.Fatalf("Evaluate refused a node that fits the capacity: %v", v)
	}
	if v := match.Fits(p.Capacity, match.Resources{}, request); v.Reason != match.ReasonAvailability {
		t.Fatalf("Fits with nothing free = %v, want availability", v)
	}
	if v := match.Evaluate(p, match.Demand{Request: match.Resources{Cores: 6}}); v.Reason != match.ReasonShape {
		t.Fatalf("Evaluate of a node larger than the capacity = %v, want shape", v)
	}
	if v := match.Fits(match.Resources{}, match.Resources{}, request); !v.OK() {
		t.Fatalf("Fits with no capacity reported = %v, want it to admit", v)
	}
}

func TestAcceptBindsTriggersToNamedRepositories(t *testing.T) {
	allow, err := sourceurl.ParseRepoAllowlist([]string{"github.com/acme/a"})
	if err != nil {
		t.Fatal(err)
	}
	p := match.Profile{Accept: allow}
	unnamed := &match.Repository{}
	other := &match.Repository{URL: "https://github.com/acme/b.git"}
	if match.Evaluate(p, match.Demand{Repo: unnamed, Trigger: true}).OK() {
		t.Error("a trigger naming no repository passed an accept list")
	}
	if !match.Evaluate(p, match.Demand{Repo: unnamed}).OK() {
		t.Error("a node of a run naming no repository was refused")
	}
	if v := match.Evaluate(p, match.Demand{Repo: other}); v.Reason != match.ReasonRepo {
		t.Errorf("a node of another repository = %v, want repo", v)
	}
}

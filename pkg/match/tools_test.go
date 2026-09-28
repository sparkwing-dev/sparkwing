package match_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestDetectToolsAdvertisesOnlyKnownToolsOnPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	onPath := map[string]bool{"terraform": true, "git": true, "make": true}
	got := match.DetectTools(func(name string) (string, error) {
		if onPath[name] {
			return "/bin/" + name, nil
		}
		return "", errors.New("not found")
	})
	for _, want := range []string{"tool:git", "tool:terraform"} {
		if !slices.Contains(got, want) {
			t.Errorf("detected %v, want %s", got, want)
		}
	}
	for _, absent := range []string{"tool:make", "tool:kubectl"} {
		if slices.Contains(got, absent) {
			t.Errorf("detected %v, which holds %s", got, absent)
		}
	}
}

func TestToolTermRefusesAnAgentWithoutTheTool(t *testing.T) {
	demand := match.Demand{Selector: []string{"tool:terraform"}}
	without := match.Profile{Labels: []string{"tool:git", "terraform"}}
	if v := match.Evaluate(without, demand); v.Reason != match.ReasonSelector || v.Term != "tool:terraform" {
		t.Fatalf("an agent without terraform = %v, want a selector refusal", v)
	}
	with := match.Profile{Labels: match.SelfAsserted([]string{"tool:terraform"})}
	if v := match.Evaluate(with, demand); !v.OK() {
		t.Fatalf("an agent advertising terraform = %v", v)
	}
	for _, tool := range match.CloudTools {
		if !match.IsKnownTool(tool) {
			t.Errorf("Cloud tool %q is not a known tool", tool)
		}
	}
}

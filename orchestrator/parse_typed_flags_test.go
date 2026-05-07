package orchestrator

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Pipeline fixtures used to exercise parseTypedFlags directly. These
// register at package init and are referenced by name from the test
// bodies below; sync.Map dedup avoids "already registered" panics
// across parallel/repeat runs.

type ptfDemoInputs struct {
	Loud   bool              `flag:"loud" short:"l" desc:"verbose"`
	Target string            `flag:"target" enum:"local,staging,prod" default:"local"`
	Token  string            `flag:"token" secret:"true"`
	Repo   string            `flag:"repo" required:"true"`
	Extras map[string]string `flag:",extra"`
}

type ptfDemoPipe struct{}

func (ptfDemoPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ ptfDemoInputs, _ sparkwing.RunContext) error {
	return nil
}

type ptfPlainInputs struct {
	X string `flag:"x"`
}

type ptfPlainPipe struct{}

func (ptfPlainPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ ptfPlainInputs, _ sparkwing.RunContext) error {
	return nil
}

func init() {
	sparkwing.Register[ptfDemoInputs]("ptf-demo", func() sparkwing.Pipeline[ptfDemoInputs] {
		return ptfDemoPipe{}
	})
	sparkwing.Register[ptfPlainInputs]("ptf-plain", func() sparkwing.Pipeline[ptfPlainInputs] {
		return ptfPlainPipe{}
	})
}

func TestParseTypedFlags_RequiredAndDefault(t *testing.T) {
	out, err := parseTypedFlags("ptf-demo", []string{"--repo", "r"})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if out["repo"] != "r" {
		t.Errorf("repo=%q", out["repo"])
	}
	if out["target"] != "local" {
		t.Errorf("default target should apply, got %q", out["target"])
	}
}

func TestParseTypedFlags_RequiredMissing(t *testing.T) {
	_, err := parseTypedFlags("ptf-demo", []string{"--target", "prod"})
	if err == nil {
		t.Fatal("expected required-missing error")
	}
}

func TestParseTypedFlags_EnumViolation(t *testing.T) {
	_, err := parseTypedFlags("ptf-demo", []string{"--repo", "r", "--target", "banana"})
	if err == nil || !contains(err.Error(), "must be one of") {
		t.Fatalf("expected enum violation, got %v", err)
	}
}

func TestParseTypedFlags_LongFormBool(t *testing.T) {
	out, err := parseTypedFlags("ptf-demo", []string{"--repo", "r", "--loud"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out["loud"] != "true" {
		t.Errorf("loud=%q, want true", out["loud"])
	}
}

func TestParseTypedFlags_ShortAlias(t *testing.T) {
	out, err := parseTypedFlags("ptf-demo", []string{"--repo", "r", "-l"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out["loud"] != "true" {
		t.Errorf("short -l should set loud=true, got %q", out["loud"])
	}
}

// The bag-forwarding path: a pipeline that opts in via `flag:",extra"`
// must accept unknown flags at the CLI boundary so they reach
// populateInputs in time to land in the map.
func TestParseTypedFlags_BagForwardsUnknown(t *testing.T) {
	out, err := parseTypedFlags("ptf-demo", []string{
		"--repo", "r",
		"--custom", "abc",
		"--another", "foo",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out["custom"] != "abc" || out["another"] != "foo" {
		keys := make([]string, 0, len(out))
		for k := range out {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		t.Fatalf("bag entries lost: out=%v keys=%v", out, keys)
	}
}

func TestParseTypedFlags_UnknownFlagWithoutBagErrors(t *testing.T) {
	_, err := parseTypedFlags("ptf-plain", []string{"--bogus", "1"})
	if err == nil || !contains(err.Error(), "unknown flag") {
		t.Fatalf("expected unknown-flag error, got %v", err)
	}
}

func TestParseTypedFlags_BoolWithEqualSign(t *testing.T) {
	out, err := parseTypedFlags("ptf-demo", []string{"--repo", "r", "--loud=false"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out["loud"] != "false" {
		t.Errorf("--loud=false should set loud to false, got %q", out["loud"])
	}
}

// Parsed args round-trip through Registration.Invoke, with bag entries
// landing in the typed Extras field. Verifies the wire-format the CLI
// produces is what populateInputs consumes.
func TestParseTypedFlags_RoundTripsThroughInvoke(t *testing.T) {
	out, err := parseTypedFlags("ptf-demo", []string{
		"--repo", "myrepo",
		"--target", "prod",
		"-l",
		"--token", "sup3r-secret",
		"--custom-key", "custom-val",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	reg, _ := sparkwing.Lookup("ptf-demo")
	plan, err := reg.Invoke(context.Background(), out, sparkwing.RunContext{Pipeline: "ptf-demo"})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if plan == nil {
		t.Fatal("plan nil")
	}
}

func contains(s, sub string) bool {
	return reflect.ValueOf(s).String() != "" && (s == sub || (len(s) >= len(sub) && (indexOf(s, sub) >= 0)))
}

func indexOf(s, sub string) int {
	if sub == "" {
		return 0
	}
	if len(sub) > len(s) {
		return -1
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

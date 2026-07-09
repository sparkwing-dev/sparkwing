package orchestrator

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

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

// parseTypedFlags is parse-only: it returns the CLI-parsed flags and does
// not inject schema defaults. The --target default is applied downstream,
// after the DefaultArgs / args: / CLI merge, so it is absent here.
func TestParseTypedFlags_NoDefaultInjection(t *testing.T) {
	out, err := parseTypedFlags("ptf-demo", []string{"--repo", "r"})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if out["repo"] != "r" {
		t.Errorf("repo=%q", out["repo"])
	}
	if _, ok := out["target"]; ok {
		t.Errorf("parse must not inject the --target default, got %q", out["target"])
	}
}

// A required flag absent from the CLI is not a parse-time error: the value
// may still arrive from the pipeline's args: block, so the required check
// runs downstream on the merged inputs rather than here.
func TestParseTypedFlags_MissingRequiredDeferred(t *testing.T) {
	out, err := parseTypedFlags("ptf-demo", []string{"--target", "prod"})
	if err != nil {
		t.Fatalf("parse-only must not reject a missing required flag, got %v", err)
	}
	if _, ok := out["repo"]; ok {
		t.Errorf("repo should be absent from parse output, got %q", out["repo"])
	}
}

// The required check lives in Invoke (populateInputs) on the merged
// argument map, so a required value supplied by anything other than the
// CLI -- e.g. the pipeline's args: block -- satisfies it, and a genuinely
// missing value still fails.
func TestInvoke_RequiredSatisfiedByMergedArgs(t *testing.T) {
	reg, _ := sparkwing.Lookup("ptf-demo")
	if _, err := reg.Invoke(context.Background(), map[string]string{"repo": "r"}, sparkwing.RunContext{Pipeline: "ptf-demo"}); err != nil {
		t.Fatalf("required repo from merged args should satisfy, got %v", err)
	}
	if _, err := reg.Invoke(context.Background(), map[string]string{}, sparkwing.RunContext{Pipeline: "ptf-demo"}); err == nil {
		t.Fatal("missing required repo should still fail at Invoke")
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

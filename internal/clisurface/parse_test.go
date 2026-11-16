package clisurface

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestParseTriggerProfilePosition(t *testing.T) {
	for _, args := range [][]string{
		{"--profile", "prod", "pipeline", "trigger", "check"},
		{"pipeline", "--profile=prod", "trigger", "check"},
		{"pipeline", "trigger", "check", "--profile", "prod"},
	} {
		got, err := Parse("sparkwing", append(args, "--", "--profile", "pipeline-value"))
		if err != nil {
			t.Fatal(err)
		}
		if got.Command.Path != "sparkwing pipeline trigger" || got.Pipeline != "check" || got.Value("profile") != "prod" || !reflect.DeepEqual(got.Args, []string{"--profile", "pipeline-value"}) {
			t.Fatalf("unexpected invocation: %#v", got)
		}
	}
}

func TestParseGrammar(t *testing.T) {
	for _, args := range [][]string{
		{"run", "check", "--sw-no-cache"},
		{"run", "check", "--profile", "prod"},
		{"run", "check", "--pipeline-arg", "value"},
		{"run", "check", "extra"},
		{"runs", "status", "123"},
		{"runs", "list", "--limit", "0"},
		{"runs", "list", "--limit", "invalid"},
		{"runs", "list", "--output", "yaml"},
		{"runs", "list", "--output="},
		{"runs", "list", "--unknown"},
		{"runs", "list", "--", "--output=json"},
		{"runs", "status"},
		{"runs", "logs", "--events-only", "--no-events"},
		{"daemon", "events", "--explain"},
		{"secrets", "set", "--secret", "NAME", "--value", "sensitive", "--file", "f"},
		{"runs", "list", "--limit", "1", "--limit", "2"},
		{"pipeline", "trigger", "check", "--detach", "--workers", "2", "--profile", "prod"},
		{"pipeline", "trigger", "--resume", "r1", "--agent", "codex", "--profile", "prod"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, err := Parse("sparkwing", args)
			var usage *UsageError
			if !errors.As(err, &usage) || usage.exitCode() != ExitUsage || !strings.HasPrefix(err.Error(), "sparkwing:") || strings.Contains(err.Error(), "sensitive") {
				t.Fatalf("expected safe usage error, got %v", err)
			}
		})
	}
	for _, args := range [][]string{
		{"run", "check", "--workers", "1", "--", "--x=1"},
		{"run", "check", "--agent", "claude-code:land-agent", "--", "--x=1"},
		{"pipeline", "trigger", "check", "--agent", "none", "--profile", "prod"},
		{"__complete", "--kind=verbs", "--word=", "-o", "plain"},
		{"pipeline", "trigger", "--resume", "r1", "--profile", "prod"},
		{"runs", "cancel", "--run", "-"},
		{"runs", "list", "-ojson", "--limit=1", "--cursor", "c"},
		{"runs", "status", "--help"},
		{"run", "--help-all"},
	} {
		if _, err := Parse("sparkwing", args); err != nil {
			t.Errorf("%v: %v", args, err)
		}
	}
}

func TestParseRemovedSurface(t *testing.T) {
	for _, args := range [][]string{
		{"crons"},
		{"serve"},
		{"cluster"},
		{"cloud"},
		{"configure"},
		{"debug", "rerun"},
		{"runs", "bounce"},
		{"runs", "consumer"},
		{"runs", "reattach"},
	} {
		if _, err := Parse("sparkwing", args); err == nil {
			t.Fatalf("removed command accepted: %v", args)
		}
	}
}

func TestParseListsPresenceAndBooleans(t *testing.T) {
	got, err := Parse("sparkwing", []string{"runs", "list", "--repo", "one,two", "--repo=three", "--follow=false"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Flags["repo"], []string{"one", "two", "three"}) || !got.Has("follow") || got.Bool("follow") {
		t.Fatalf("presence or repetition lost: %#v", got.Flags)
	}
	if _, err := Parse("sparkwing", []string{"runs", "list", "--repo=one,,two"}); err == nil {
		t.Fatal("empty list member accepted")
	}
}

func TestParseEveryCommandHelp(t *testing.T) {
	for _, command := range Registry() {
		t.Run(command.Path, func(t *testing.T) {
			words := strings.Fields(command.Path)
			inv, err := Parse(words[0], append(words[1:], "--help", "-o", "json"))
			if err != nil || inv.Command.Path != command.Path || !inv.Help() {
				t.Fatalf("help parse: %#v, %v", inv, err)
			}
		})
	}
}

func TestRunCacheProfileMayExplicitlyDisableMachineDefault(t *testing.T) {
	inv, err := Parse("sparkwing", []string{"run", "ci", "--cache-from="})
	if err != nil || !inv.Has("cache-from") || inv.Value("cache-from") != "" {
		t.Fatalf("explicit empty cache profile=%+v %v", inv, err)
	}
	for _, args := range [][]string{{"run", "ci", "--publish="}} {
		if _, err := Parse("sparkwing", args); err == nil {
			t.Fatalf("accepted empty required option: %v", args)
		}
	}
}

func TestControllerBreakGlassRequiresOperator(t *testing.T) {
	for _, verb := range []string{"freeze", "unfreeze"} {
		args := []string{"admin", verb, "--team=a", "--reason=investigation"}
		if _, err := Parse("sparkwing-controller", args); err == nil {
			t.Errorf("%s accepted no operator", verb)
		}
		args = append(args, "--operator=account-1")
		inv, err := Parse("sparkwing-controller", args)
		if err != nil || inv.Value("operator") != "account-1" {
			t.Errorf("%s rejected operator identity: %v", verb, err)
		}
		if _, err := Parse("sparkwing-controller", append(args, "--credentials-dir=/old")); err == nil {
			t.Errorf("%s accepted removed credentials-dir", verb)
		}
	}
}

func TestRefusedFlagHintNamesWhereTheFlagBelongs(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"update", "--check"}, "unknown flag --check; sparkwing update takes --dry-run, --force, --override-hold, --version (see --help)"},
		{[]string{"run", "release", "--bump", "patch"}, "unknown flag --bump; pipeline arguments follow --"},
		{[]string{"run", "release", "--version", "v0.68.0"}, "flag --version is not accepted; pipeline arguments follow --"},
	} {
		_, err := Parse("sparkwing", tc.args)
		if err == nil || !strings.HasSuffix(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want suffix %q", tc.args, err, tc.want)
		}
	}
}

func TestBareVersionFlagSelectsTheVersionCommand(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"--version", "-o", "json"}, {"-o", "json", "--version"}} {
		inv, err := Parse("sparkwing", args)
		if err != nil || inv.Command.Path != "sparkwing version" {
			t.Errorf("%v: command %q, err %v; want sparkwing version", args, inv.Command.Path, err)
		}
	}
	if _, err := Parse("sparkwing", []string{"runs", "--version"}); err == nil {
		t.Error("--version after a command word selected the version command")
	}
}

func TestParseReadsOnlyGlobalFlagsBeforeTheCommand(t *testing.T) {
	for args, want := range map[string]string{
		"--version=x":           "unknown flag --version; use the version command",
		"--tail 5 runs list":    "unknown flag --tail; a command's flags follow the command",
		"commands --query x":    "unknown flag --query; sparkwing commands takes --search, --limit, --cursor, --format, --split-dir, --path, --all (see --help)",
		"run check --bogus":     "unknown flag --bogus; pipeline arguments follow --",
		"runs --run 1 status":   "unknown flag --run; a command's flags follow the command",
		"-o json runs list":     "",
		"--profile p runs list": "",
	} {
		_, err := Parse("sparkwing", strings.Fields(args))
		var usage *UsageError
		if want == "" {
			if err != nil {
				t.Errorf("%s: %v", args, err)
			}
			continue
		}
		if !errors.As(err, &usage) || usage.Message != want {
			t.Errorf("%s: error %v, want %q", args, err, want)
		}
	}
}

func TestMisspelledCommandSuggestsNearestSibling(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"runs", "stauts"}, "sparkwing runs status"},
		{[]string{"runz", "list"}, "sparkwing runs"},
		{[]string{"pipeline", "triger", "check"}, "sparkwing pipeline trigger"},
	} {
		_, err := Parse("sparkwing", tc.args)
		if err == nil || !strings.Contains(err.Error(), "did you mean "+tc.want) {
			t.Errorf("%v: want suggestion %q, got %v", tc.args, tc.want, err)
		}
	}
	_, err := Parse("sparkwing", []string{"runs", "status", "private-run-value"})
	if err == nil || strings.Contains(err.Error(), "private-run-value") || strings.Contains(err.Error(), "did you mean") {
		t.Fatalf("positional value treated as command: %v", err)
	}
}

func TestLintRejectsRemovedStaticFlag(t *testing.T) {
	if _, err := Parse("sparkwing", []string{"pipeline", "lint", "--static"}); err == nil {
		t.Fatal("accepted inert --static")
	}
}

func TestRunInvocationCloudRouting(t *testing.T) {
	for _, args := range [][]string{
		{"run", "gate", "--route", "cloud", "--profile", "work", "--detach", "--", "--full"},
		{"--profile", "work", "run", "gate", "--route", "cloud"},
		{"run", "gate", "--route", "local"},
	} {
		if _, err := Parse("sparkwing", args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	for _, args := range [][]string{
		{"run", "gate", "--profile", "work"},
		{"run", "gate", "--profile", "work", "--route", "local"},
		{"run", "gate", "--route", "other"},
		{"run", "gate", "--allow-secret-file", "a.key"},
		{"run", "gate", "--route", "cloud", "--allow-secret-file", "a.key"},
		{"pipeline", "trigger", "gate", "--profile", "work", "--working-tree", "--allow-secret-file", "a.key"},
		{"run", "gate", "--route", "local", "--allow-secret-file", "a.key"},
	} {
		if _, err := Parse("sparkwing", args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestInvocationOutput(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		for _, format := range []string{"", "pretty", "json", "plain"} {
			args := []string{"info"}
			if format != "" {
				args = append(args, "-o", format)
			}
			inv, err := Parse("sparkwing", args)
			if err != nil {
				t.Fatal(err)
			}
			want := format
			if want == "" {
				want = "json"
				if terminal {
					want = "pretty"
				}
			}
			if got := inv.Output(terminal); got != want {
				t.Fatalf("format=%q terminal=%t: got %q, want %q", format, terminal, got, want)
			}
		}
	}
}

func TestMisspelledFlagsAndValuesSuggestTheNearestSpelling(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"run", "build", "--sw-dry-run"}, "unknown flag --sw-dry-run; did you mean --dry-run? Pipeline arguments follow --"},
		{[]string{"run", "build", "--sw-detached"}, "unknown flag --sw-detached; did you mean --detach? Pipeline arguments follow --"},
		{[]string{"run", "build", "--sw-allow-risk", "prod"}, "unknown flag --sw-allow-risk; did you mean --allow-risk? Pipeline arguments follow --"},
		{[]string{"pipeline", "plan", "--pipeline", "build", "--sw-dry-run"}, "unknown flag --sw-dry-run; did you mean --dry-run? Pipeline arguments follow --"},
		{[]string{"run", "build", "--sw-dryrun"}, "unknown flag --sw-dryrun; did you mean --dry-run? Pipeline arguments follow --"},
		{[]string{"runs", "list", "--limt", "5"}, "unknown flag --limt; did you mean --limit?"},
		{[]string{"runs", "status", "run-deadbeef"}, "unexpected positional argument; pass it as --run RUN_ID"},
		{[]string{"runs", "list", "extra"}, "unexpected positional argument; sparkwing runs list takes flags only (see sparkwing runs list --help)"},
	} {
		_, err := Parse("sparkwing", tc.args)
		var usage *UsageError
		if !errors.As(err, &usage) || usage.Message != tc.want {
			t.Errorf("%v: error %v, want %q", tc.args, err, tc.want)
		}
	}
}

func TestRunStatusesMatchStoredRunStates(t *testing.T) {
	var want []string
	for _, state := range runstate.RunStates() {
		want = append(want, string(state))
	}
	if !slices.Equal(RunStatuses(), want) {
		t.Fatalf("RunStatuses() = %v, want %v", RunStatuses(), want)
	}
}

func TestCommonGuessesSuggestTheRealCommandWithoutRunningIt(t *testing.T) {
	registered := map[string]bool{}
	for _, command := range Registry() {
		registered[command.Path] = !command.Hidden
	}
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"runs", "show", "--run", "r1"}, []string{"sparkwing runs status"}},
		{[]string{"pipelines", "list"}, []string{"sparkwing pipeline list"}},
		{[]string{"pipelines"}, []string{"sparkwing pipeline"}},
		{[]string{"pipeline", "show", "--pipeline", "build"}, []string{"sparkwing pipeline describe"}},
		{[]string{"secrets", "remove", "--secret", "X"}, []string{"sparkwing secrets delete"}},
		{[]string{"secrets", "add", "--secret", "X"}, []string{"sparkwing secrets set"}},
		{[]string{"profiles", "delete", "--profile", "p"}, []string{"sparkwing profiles remove", ""}},
		{[]string{"profiles", "rm", "--profile", "p"}, []string{"sparkwing profiles remove", "sparkwing profiles delete"}},
		{[]string{"schedules", "delete", "--schedule", "s"}, []string{"sparkwing schedules uninstall"}},
		{[]string{"teams"}, []string{"sparkwing team"}},
		{[]string{"teams", "settings"}, []string{"sparkwing team settings"}},
		{[]string{"logout"}, []string{"sparkwing profiles remove", "sparkwing profiles delete"}},
		{[]string{"tokens", "ls"}, []string{"sparkwing tokens list"}},
	} {
		_, err := Parse("sparkwing", tc.args)
		if err == nil {
			if !slices.Contains(tc.want, "") {
				t.Errorf("%v ran instead of suggesting %v", tc.args, tc.want)
			}
			continue
		}
		var usage *UsageError
		if !errors.As(err, &usage) {
			t.Errorf("%v: %v is not a usage error", tc.args, err)
			continue
		}
		suggested := ""
		if _, rest, ok := strings.Cut(usage.Message, "did you mean "); ok {
			suggested, _, _ = strings.Cut(rest, "?")
		}
		if !slices.Contains(tc.want, suggested) || !registered[suggested] {
			t.Errorf("%v: suggested %q, want one of %v that is registered: %s", tc.args, suggested, tc.want, usage.Message)
		}
	}
}

func TestSameNamedFlagsKeepTheirOwnCommandsArity(t *testing.T) {
	inv, err := Parse("sparkwing", []string{"pools", "allow", "--pool", "prod", "--repo", "acme/api", "--route", "-o", "json"})
	if err != nil || !inv.Bool("route") || inv.Value("output") != "json" {
		t.Fatalf("pools allow --route: %v %+v", err, inv.Flags)
	}
	inv, err = Parse("sparkwing", []string{"run", "build", "--route", "local", "-o", "json"})
	if err != nil || inv.Value("route") != "local" || inv.Value("output") != "json" {
		t.Fatalf("run --route: %v %+v", err, inv.Flags)
	}
}

package clisurface

import (
	"errors"
	"reflect"
	"strings"
	"testing"
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
		{"pipeline", "trigger", "check", "--detach", "--sw-workers", "2", "--profile", "prod"},
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
		{"run", "check", "--sw-workers", "1", "--", "--x=1"},
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
	inv, err := Parse("sparkwing", []string{"run", "ci", "--sw-cache-from="})
	if err != nil || !inv.Has("sw-cache-from") || inv.Value("sw-cache-from") != "" {
		t.Fatalf("explicit empty cache profile=%+v %v", inv, err)
	}
	for _, args := range [][]string{{"run", "ci", "--sw-output="}, {"run", "ci", "--publish="}} {
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

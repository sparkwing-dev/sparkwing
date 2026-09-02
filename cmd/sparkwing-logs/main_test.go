package main

import (
	"strings"
	"testing"
)

func TestRunRefusesToStartWithoutAControllerWhenAuthIsRequired(t *testing.T) {
	t.Setenv("SPARKWING_CONTROLLER_URL", "")
	for _, tc := range []struct {
		name string
		env  string
		args []string
	}{
		{name: "flag", args: []string{"--require-auth"}},
		{name: "env", env: "1", args: nil},
		{name: "env word", env: "true", args: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SPARKWING_REQUIRE_AUTH", tc.env)
			err := run(tc.args)
			if err == nil {
				t.Fatal("run started an unauthenticated logs service with auth required")
			}
			if !strings.Contains(err.Error(), "--require-auth") {
				t.Errorf("err = %v, want it to name --require-auth", err)
			}
		})
	}
}

func TestEnvTruthyReadsOnlyExplicitOptIns(t *testing.T) {
	for value, want := range map[string]bool{
		"1": true, "true": true, "YES": true, " on ": true,
		"": false, "0": false, "false": false, "maybe": false,
	} {
		t.Setenv("SPARKWING_REQUIRE_AUTH", value)
		if got := envTruthy("SPARKWING_REQUIRE_AUTH"); got != want {
			t.Errorf("envTruthy(%q) = %v, want %v", value, got, want)
		}
	}
}

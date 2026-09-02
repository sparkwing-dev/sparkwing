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

func TestRunRefusesAControllerURLItCouldNeverResolveTokensAgainst(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
	}{
		{name: "blank", url: "   "},
		{name: "no scheme", url: "controller.example.com"},
		{name: "no host", url: "http://"},
		{name: "wrong scheme", url: "ftp://controller.example.com"},
		{name: "a path, not a URL", url: "/var/run/controller.sock"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SPARKWING_CONTROLLER_URL", "")
			t.Setenv("SPARKWING_REQUIRE_AUTH", "")
			err := run([]string{"--require-auth", "--controller", tc.url})
			if err == nil {
				t.Fatalf("run started with --controller %q; health would advertise auth enabled while every whoami fails", tc.url)
			}
			if !strings.Contains(err.Error(), "--require-auth") {
				t.Errorf("err = %v, want it to name --require-auth", err)
			}
		})
	}
}

func TestCheckControllerURLAcceptsAbsoluteHTTPURLs(t *testing.T) {
	for _, url := range []string{"http://controller.default.svc.cluster.local", "https://controller.example.com/base"} {
		if err := checkControllerURL(url); err != nil {
			t.Errorf("checkControllerURL(%q) = %v, want it accepted", url, err)
		}
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

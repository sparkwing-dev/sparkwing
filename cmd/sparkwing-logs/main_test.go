package main

import (
	"strings"
	"testing"
)

func TestRunRefusesToStartWithoutAControllerWhenAuthIsRequired(t *testing.T) {
	err := run([]string{"--require-auth"})
	if err == nil {
		t.Fatal("run started an unauthenticated logs service with auth required")
	}
	if !strings.Contains(err.Error(), "--require-auth") {
		t.Errorf("err = %v, want it to name --require-auth", err)
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

func TestRunRejectsMalformedLimitFlags(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flag  string
		value string
	}{
		{name: "duration with a day suffix", flag: "--retention", value: "7d"},
		{name: "byte count with a unit", flag: "--max-node-bytes", value: "64MiB"},
		{name: "ratio that is not a number", flag: "--binary-ratio", value: "a third"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := run([]string{tc.flag, tc.value})
			if err == nil {
				t.Fatalf("run accepted %s=%q and fell back to the default bound", tc.flag, tc.value)
			}
			if !strings.Contains(err.Error(), strings.TrimPrefix(tc.flag, "--")) {
				t.Errorf("err = %v, want it to name %s", err, tc.flag)
			}
		})
	}
}

func TestRunRejectsNegativeLimitFlags(t *testing.T) {
	for _, flag := range []string{"--max-node-bytes", "--max-run-bytes", "--max-inflight-bytes", "--retention"} {
		value := "-1"
		if flag == "--retention" {
			value = "-1h"
		}
		err := run([]string{flag, value})
		if err == nil {
			t.Fatalf("run accepted %s=%s, which turns that bound off instead of failing", flag, value)
		}
		if !strings.Contains(err.Error(), flag) {
			t.Errorf("err = %v, want it to name %s", err, flag)
		}
	}
}

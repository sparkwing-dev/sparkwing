package main

import (
	"strings"
	"testing"
)

func TestParseFixtureConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    []string
		want    fixtureConfig
		wantErr string
	}{
		{
			name: "split values preserve spaces",
			args: []string{
				"--fixture-home", "/tmp/browser fixture/home",
				"--web-out", "/tmp/browser fixture/web out",
			},
			want: fixtureConfig{
				home:   "/tmp/browser fixture/home",
				webOut: "/tmp/browser fixture/web out",
			},
		},
		{
			name: "equals values preserve spaces",
			args: []string{
				"--fixture-home=/tmp/browser fixture/home",
				"--web-out=/tmp/browser fixture/web out",
			},
			want: fixtureConfig{
				home:   "/tmp/browser fixture/home",
				webOut: "/tmp/browser fixture/web out",
			},
		},
		{
			name: "duplicate fixture home across split and equals forms",
			args: []string{
				"--fixture-home", "first",
				"--fixture-home=second",
				"--web-out", "web",
			},
			wantErr: "--fixture-home may only be set once",
		},
		{
			name: "duplicate web out across equals and split forms",
			args: []string{
				"--fixture-home=home",
				"--web-out=first",
				"--web-out", "second",
			},
			wantErr: "--web-out may only be set once",
		},
		{
			name:    "missing fixture home",
			args:    []string{"--web-out", "web"},
			wantErr: "--fixture-home is required",
		},
		{
			name:    "missing web out",
			args:    []string{"--fixture-home", "home"},
			wantErr: "--web-out is required",
		},
		{
			name:    "split form without value",
			args:    []string{"--fixture-home"},
			wantErr: "flag needs an argument: -fixture-home",
		},
		{
			name:    "empty equals value",
			args:    []string{"--fixture-home=", "--web-out=web"},
			wantErr: "--fixture-home requires a non-empty value",
		},
		{
			name:    "unknown flag",
			args:    []string{"--fixture-home=home", "--web-out=web", "--other=value"},
			wantErr: "flag provided but not defined: -other",
		},
		{
			name:    "positional argument",
			args:    []string{"--fixture-home=home", "--web-out=web", "extra"},
			wantErr: `unexpected browser fixture argument "extra"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseFixtureConfig(test.args)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("parseFixtureConfig(%q) error = %v, want %q", test.args, err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseFixtureConfig(%q): %v", test.args, err)
			}
			if got != test.want {
				t.Fatalf("parseFixtureConfig(%q) = %#v, want %#v", test.args, got, test.want)
			}
		})
	}
}

func TestSeedRequestNormalize(t *testing.T) {
	t.Parallel()
	defaulted := seedRequest{Count: 2}
	if err := defaulted.normalize(); err != nil {
		t.Fatalf("normalize defaults: %v", err)
	}
	if defaulted.Pipeline != "fixture" || defaulted.Branch != "main" || defaulted.Status != "success" || defaulted.StepSeconds != 60 || defaulted.IDPrefix != "seeded-" || defaulted.Start.IsZero() {
		t.Fatalf("normalize defaults = %#v", defaulted)
	}
	for _, test := range []struct {
		name    string
		request seedRequest
		wantErr string
	}{
		{name: "no runs", request: seedRequest{}, wantErr: "count must be between"},
		{name: "unknown status", request: seedRequest{Count: 1, Status: "running"}, wantErr: "status must be success or failed"},
		{name: "negative step", request: seedRequest{Count: 1, StepSeconds: -1}, wantErr: "step_seconds must be positive"},
		{name: "named id for many runs", request: seedRequest{Count: 2, ID: "one"}, wantErr: "count must be 1"},
	} {
		if err := test.request.normalize(); err == nil || !strings.Contains(err.Error(), test.wantErr) {
			t.Errorf("%s: normalize error = %v, want %q", test.name, err, test.wantErr)
		}
	}
}

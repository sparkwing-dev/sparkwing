package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

// Every row is a misdeclared deployment that used to start in some weaker
// mode, or to write global settings before failing. Each must refuse before
// the store is opened, so nothing is created or changed.
func TestStartupMatrixRefusesBeforeTouchingTheStore(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = taken.Close() })
	for _, tc := range []struct {
		name  string
		files map[string]string
		args  []string
		want  string
	}{
		{"misnamed pg-url", map[string]string{"pg_url": "postgres://db/sparkwing"}, nil, "pg_url"},
		{"postgres without pg-url", nil, []string{"--store=postgres"}, "pg-url"},
		{"pg-url under sqlite", map[string]string{credPGURL: "postgres://db/sparkwing"}, nil, "--store=postgres"},
		{"cloud on sqlite", nil, []string{"--deployment=cloud"}, "--store=postgres"},
		{"team without a license", nil, []string{"--deployment=team"}, "license"},
		{"unverifiable license", map[string]string{credLicense: "garbage"}, []string{"--deployment=team"}, "license credential"},
		{"unverifiable license on a local deployment", map[string]string{credLicense: "garbage"}, nil, "license credential"},
		{"unknown kind", nil, []string{"--deployment=teams"}, "--deployment"},
		{"unknown store", nil, []string{"--store=postgresql"}, "--store"},
		{"cloud limits off the cloud", nil, []string{"--limits-profile=cloud"}, "--deployment=cloud"},
		{"previous webhook secret alone", map[string]string{credGitHubAppPreviousHook: "whsec"}, nil, credGitHubAppPreviousHook},
		{
			"payment mode, then a bad flag",
			map[string]string{credStripeAPIKey: "sk_test_x", credStripeWebhookSecret: "whsec_x"},
			[]string{"--payment-mode=test", "--bucket-measure-pages=0"},
			"--bucket-measure-pages",
		},
		{"free slots, then a bad flag", nil, []string{"--free-team-slots=5", "--log-retention=nope"}, "--log-retention"},
		{"external origin runners would refuse", nil, []string{"--external-url=http://controller.example.com"}, "unencrypted"},
		{
			"malformed listen address",
			map[string]string{credStripeAPIKey: "sk_test_x", credStripeWebhookSecret: "whsec_x"},
			[]string{"--payment-mode=test", "--addr=nope"},
			"--addr",
		},
		{"occupied port", nil, []string{"--free-team-slots=5", "--addr=" + taken.Addr().String()}, "--addr"},
		{"occupied metrics port", nil, []string{"--free-team-slots=5", "--metrics-addr=" + taken.Addr().String()}, "--metrics-addr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SPARKWING_HOME", t.TempDir())
			dir := t.TempDir()
			for name, value := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			args := append([]string{"--addr=127.0.0.1:0", "--credentials-dir=" + dir}, tc.args...)
			err := run(args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("run(%q) = %v, want a refusal naming %q", args, err, tc.want)
			}
			p, perr := paths.DefaultPaths()
			if perr != nil {
				t.Fatal(perr)
			}
			if _, serr := os.Stat(p.StateDB()); !errors.Is(serr, os.ErrNotExist) {
				t.Fatalf("a refused start opened the store: %v", serr)
			}
		})
	}
}

package main

import (
	"strings"
	"testing"
)

func TestCloudOperationsAreAbsentFromPublicCLI(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func([]string) error
		args []string
	}{
		{"credits", runCluster, []string{"credits", "show"}},
		{"set-metered", runTokens, []string{"set-metered", "--prefix", "swr_123", "--metered", "true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run(tc.args)
			if err == nil || !strings.Contains(err.Error(), "unknown subcommand") {
				t.Fatalf("%v returned %v, want unknown subcommand", tc.args, err)
			}
		})
	}

	for _, path := range []string{"sparkwing cluster credits", "sparkwing cluster tokens set-metered"} {
		if err := runCommands([]string{"--path", path, "--output", "plain"}); err == nil {
			t.Errorf("public command index still advertises %s", path)
		}
	}
	if err := runTokensCreate([]string{"--metered", "--profile", "none"}); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("tokens create --metered returned %v, want unknown flag", err)
	}
}

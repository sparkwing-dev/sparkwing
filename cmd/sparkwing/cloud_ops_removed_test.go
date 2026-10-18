package main

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
)

func TestCloudOperationsAreAbsentFromPublicCLI(t *testing.T) {
	for _, args := range [][]string{
		{"cluster", "credits", "show"},
		{"tokens", "set-metered", "--token", "swr_123", "--metered", "true"},
		{"tokens", "create", "--metered", "--profile", "none"},
	} {
		if _, err := clisurface.Parse("sparkwing", args); err == nil {
			t.Fatalf("accepted removed operation %v", args)
		}
	}
	for _, path := range []string{"sparkwing cluster credits", "sparkwing cluster tokens set-metered"} {
		if err := runCommandsArgs([]string{"--path", path, "--output", "plain"}); err == nil {
			t.Errorf("public command index still advertises %s", path)
		}
	}
}

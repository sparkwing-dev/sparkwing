package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func TestUpgradeVerifierRefusesUnusableCommandIndex(t *testing.T) {
	for _, body := range []string{"", "not json", `{"path":"sparkwing pipeline show"} trailing`, `{"path":"sparkwing pipeline plan"}`, `{"kind":"page","total":0}`} {
		t.Run(body, func(t *testing.T) {
			v := upgradeVerifier{exec: func(context.Context, string, procenv.Env, string, ...string) ([]byte, error) {
				return []byte(body), nil
			}}
			if _, err := v.pipelineReadVerb(t.Context(), t.TempDir(), procenv.Env{}, filepath.Join(t.TempDir(), "engine")); err == nil {
				t.Fatalf("accepted unusable command index %q", body)
			}
		})
	}
	failure := errors.New("selected engine is unavailable")
	v := upgradeVerifier{exec: func(context.Context, string, procenv.Env, string, ...string) ([]byte, error) {
		return nil, failure
	}}
	if _, err := v.pipelineReadVerb(t.Context(), t.TempDir(), procenv.Env{}, filepath.Join(t.TempDir(), "engine")); !errors.Is(err, failure) || !strings.Contains(err.Error(), "selected engine commands") {
		t.Fatalf("lost index failure: %v", err)
	}
}

package main

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
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

func TestUpgradeVerifierContinuesCommandIndexPages(t *testing.T) {
	calls := 0
	v := upgradeVerifier{exec: func(_ context.Context, _ string, _ procenv.Env, _ string, args ...string) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte("{\"path\":\"sparkwing pipeline plan\"}\n{\"kind\":\"page\",\"next_cursor\":\"sparkwing pipeline plan\"}\n"), nil
		}
		if calls != 2 || !slices.Equal(args[len(args)-2:], []string{"--cursor", "sparkwing pipeline plan"}) {
			t.Fatalf("command index continuation: %v", args)
		}
		return []byte(`{"path":"sparkwing pipeline show"}`), nil
	}}
	verb, err := v.pipelineReadVerb(t.Context(), t.TempDir(), procenv.Env{}, filepath.Join(t.TempDir(), "engine"))
	if err != nil || verb != "show" || calls != 2 {
		t.Fatalf("verb=%s calls=%d error=%v", verb, calls, err)
	}
}

func TestUpgradeVerifierRejectsCyclingCommandIndex(t *testing.T) {
	calls := 0
	v := upgradeVerifier{exec: func(context.Context, string, procenv.Env, string, ...string) ([]byte, error) {
		calls++
		if calls > 3 {
			t.Fatal("command index followed a repeated cursor")
		}
		cursor := "a"
		if calls == 2 {
			cursor = "b"
		}
		return []byte(`{"path":"sparkwing pipeline describe"}` + "\n" + `{"kind":"page","next_cursor":"` + cursor + `"}`), nil
	}}
	_, err := v.pipelineReadVerb(t.Context(), t.TempDir(), procenv.Env{}, filepath.Join(t.TempDir(), "engine"))
	if err == nil || !strings.Contains(err.Error(), "repeated cursor") || calls != 3 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}

func TestUpgradeVerifierCapsCommandIndexPages(t *testing.T) {
	cursors := []string{"a", "b", "c", "d"}
	calls := 0
	v := upgradeVerifier{exec: func(context.Context, string, procenv.Env, string, ...string) ([]byte, error) {
		calls++
		if calls > len(cursors) {
			t.Fatal("command index exceeded four pages")
		}
		return []byte(`{"path":"sparkwing pipeline describe"}` + "\n" + `{"kind":"page","next_cursor":"` + cursors[calls-1] + `"}`), nil
	}}
	_, err := v.pipelineReadVerb(t.Context(), t.TempDir(), procenv.Env{}, filepath.Join(t.TempDir(), "engine"))
	if err == nil || !strings.Contains(err.Error(), "page limit") || calls != 4 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}

func TestUpgradeVerifierStopsCommandIndexAtShow(t *testing.T) {
	calls := 0
	v := upgradeVerifier{exec: func(context.Context, string, procenv.Env, string, ...string) ([]byte, error) {
		calls++
		if calls > 1 {
			t.Fatal("command index continued after advertising show")
		}
		return []byte("{\"path\":\"sparkwing pipeline show\"}\n{\"kind\":\"page\",\"next_cursor\":\"a\"}"), nil
	}}
	verb, err := v.pipelineReadVerb(t.Context(), t.TempDir(), procenv.Env{}, filepath.Join(t.TempDir(), "engine"))
	if err != nil || verb != "show" || calls != 1 {
		t.Fatalf("verb=%s calls=%d error=%v", verb, calls, err)
	}
}

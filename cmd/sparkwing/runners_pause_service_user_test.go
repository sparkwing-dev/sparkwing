//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	runnerservice "github.com/sparkwing-dev/sparkwing/internal/runners/service"
)

func TestRunnersPauseServiceUserRelaysThroughThePrivilegedStep(t *testing.T) {
	previousEUID := serviceUserEUID
	serviceUserEUID = func() int { return 1000 }
	t.Cleanup(func() { serviceUserEUID = previousEUID })
	var timeline []string
	p := &fakePrivilege{timeline: &timeline, reply: func([]string, []byte) ([]byte, error) {
		return json.Marshal(runnerPauseResult{Runner: "desk", Paused: true, Changed: true})
	}}
	swapServiceUserPrivilege(t, p)
	inv, err := clisurface.Parse("sparkwing", []string{"runners", "pause", "--service-user", "sw-ci", "-o", "pretty"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := setRunnerPaused(inv, true, &out); err != nil {
		t.Fatal(err)
	}
	privileged := "sudo"
	if !slices.Equal(timeline, []string{privileged + " -v", privileged + " runners pause --service-user sw-ci --output json"}) {
		t.Fatalf("timeline = %q", timeline)
	}
	if got := out.String(); !strings.Contains(got, "Paused runner desk") || !strings.Contains(got, "sparkwing runners resume --service-user sw-ci") {
		t.Fatalf("pause output = %q", got)
	}
}

func TestRunnersPauseServiceUserWritesTheMarkerAsTheAccount(t *testing.T) {
	fx := newInstallFixture(t)
	credentials := filepath.Join(fx.home, ".sparkwing", "credentials")
	if err := os.MkdirAll(credentials, 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(fx.home, ".config", "sparkwing", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("runner:\n  controller: https://controller.example\n  holder_prefix: desk\n  credentials_dir: "+credentials+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	serviceUserRunAs = func(_ context.Context, account runnerservice.Account, args []string, _ []byte) ([]byte, error) {
		fx.runAs = append(fx.runAs, account.Name+" "+strings.Join(args[1:], " "))
		t.Setenv("HOME", account.Home)
		inv, err := clisurface.Parse("sparkwing", args[1:])
		if err != nil {
			return nil, err
		}
		var runErr error
		out := captureStdout(t, func() { runErr = runRunnerAccountPause(inv) })
		return []byte(out), runErr
	}
	run := func(verb string) runnerPauseResult {
		t.Helper()
		inv, err := clisurface.Parse("sparkwing", []string{"runners", verb, "--service-user", "sw-ci", "-o", "json"})
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := setRunnerPaused(inv, verb == "pause", &out); err != nil {
			t.Fatal(err)
		}
		var result runnerPauseResult
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	if got := run("pause"); got != (runnerPauseResult{Runner: "desk", Paused: true, Changed: true}) {
		t.Fatalf("pause = %+v", got)
	}
	if paused, err := runners.Paused(credentials); err != nil || !paused {
		t.Fatalf("the account's runner is not paused: %v %v", paused, err)
	}
	if got := run("resume"); got != (runnerPauseResult{Runner: "desk", Paused: false, Changed: true}) {
		t.Fatalf("resume = %+v", got)
	}
	if paused, _ := runners.Paused(credentials); paused {
		t.Fatal("resume left the marker")
	}
	if !slices.Equal(fx.runAs, []string{"sw-ci __runner-account-pause", "sw-ci __runner-account-pause --resume"}) {
		t.Fatalf("account steps = %q", fx.runAs)
	}
}

func TestRunnersPauseServiceUserConflictsWithConfig(t *testing.T) {
	if _, err := clisurface.Parse("sparkwing", []string{"runners", "pause", "--service-user", "sw-ci", "--config", "/etc/sparkwing.yaml"}); err == nil {
		t.Fatal("--service-user and --config parsed together")
	}
}

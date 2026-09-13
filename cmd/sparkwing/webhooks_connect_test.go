package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type fakeGitHub struct {
	t          *testing.T
	calls      []string
	hooks      []githubHook
	deliveries []githubDelivery
	nextHookID int64
	// safety: the status the fake reports for the ping delivery, so a test
	// can drive the controller-refused case as well as the accepted one.
	pingStatus int
	secrets    []string
}

type connectFixture struct {
	gh      *fakeGitHub
	store   *store.Store
	repo    string
	control *httptest.Server
}

func newConnectFixture(t *testing.T) *connectFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake gh on PATH is a POSIX executable")
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	admin, _, err := st.CreateToken("operator", store.TokenKindUser,
		[]string{controller.ScopeAdmin}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(srv.Close)
	writeProfilesFixture(t, fmt.Sprintf(
		"profiles:\n  prod:\n    controller: { url: %s, token: %s }\n", srv.URL, admin))

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write the fake gh: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	gh := &fakeGitHub{t: t, nextHookID: 5000, pingStatus: 200}
	prev := runGH
	runGH = gh.run
	t.Cleanup(func() { runGH = prev })

	prevAttempts, prevInterval := webhookPingAttempts, webhookPingInterval
	webhookPingAttempts, webhookPingInterval = 2, time.Millisecond
	t.Cleanup(func() { webhookPingAttempts, webhookPingInterval = prevAttempts, prevInterval })

	return &connectFixture{gh: gh, store: st, repo: "acme/widgets", control: srv}
}

func (g *fakeGitHub) run(stdin []byte, args ...string) ([]byte, error) {
	call := strings.Join(args, " ")
	g.calls = append(g.calls, call)
	switch {
	case strings.HasSuffix(call, "/hooks") && !strings.Contains(call, "-X"):
		return json.Marshal(g.hooks)
	case strings.Contains(call, "-X POST") && strings.HasSuffix(call, "/hooks --input -"):
		var payload webhookHookPayload
		if err := json.Unmarshal(stdin, &payload); err != nil {
			g.t.Fatalf("create hook payload: %v", err)
		}
		g.secrets = append(g.secrets, payload.Config.Secret)
		g.nextHookID++
		hook := githubHook{
			ID:     g.nextHookID,
			Active: payload.Active,
			Events: payload.Events,
			Config: githubHookConfig{URL: payload.Config.URL, ContentType: payload.Config.ContentType},
		}
		g.hooks = append(g.hooks, hook)
		return json.Marshal(hook)
	case strings.Contains(call, "-X PATCH"):
		var payload webhookHookPayload
		if err := json.Unmarshal(stdin, &payload); err != nil {
			g.t.Fatalf("update hook payload: %v", err)
		}
		g.secrets = append(g.secrets, payload.Config.Secret)
		return nil, nil
	case strings.HasSuffix(call, "/pings"):
		g.deliveries = append(g.deliveries, githubDelivery{
			GUID:        fmt.Sprintf("ping-%d", len(g.deliveries)+1),
			Event:       "ping",
			StatusCode:  g.pingStatus,
			Status:      "OK",
			DeliveredAt: time.Now().UTC().Format(time.RFC3339),
		})
		return nil, nil
	case strings.Contains(call, "/deliveries"):
		return json.Marshal(g.deliveries)
	case strings.Contains(call, "-X DELETE"):
		id := call[strings.LastIndex(call, "/")+1:]
		kept := g.hooks[:0]
		for _, h := range g.hooks {
			if fmt.Sprintf("%d", h.ID) != id {
				kept = append(kept, h)
			}
		}
		g.hooks = kept
		return nil, nil
	}
	g.t.Fatalf("the fake gh was asked something it does not model: gh %s", call)
	return nil, nil
}

func (g *fakeGitHub) sawSecretInArgv(secret string) bool {
	for _, call := range g.calls {
		if strings.Contains(call, secret) {
			return true
		}
	}
	return false
}

func TestWebhooksConnect_RegistersBothSidesAndVerifiesThePing(t *testing.T) {
	f := newConnectFixture(t)
	var err error
	out := captureStdout(t, func() {
		err = runWebhooksConnect([]string{
			"--profile", "prod", "--repo", f.repo, "--pipeline", "build",
		})
	})
	if err != nil {
		t.Fatalf("connect: %v\n%s", err, out)
	}

	if len(f.gh.hooks) != 1 {
		t.Fatalf("hooks on the repository = %d, want one", len(f.gh.hooks))
	}
	hook := f.gh.hooks[0]
	wantURL := f.control.URL + "/webhooks/github/build"
	if hook.Config.URL != wantURL {
		t.Errorf("hook url = %q, want %q", hook.Config.URL, wantURL)
	}
	if hook.Config.ContentType != "json" || !hook.Active {
		t.Errorf("hook = %+v, want an active json hook", hook)
	}
	if strings.Join(hook.Events, ",") != defaultWebhookEvents {
		t.Errorf("events = %v, want %s", hook.Events, defaultWebhookEvents)
	}

	if len(f.gh.secrets) == 0 || len(f.gh.secrets[0]) != 64 {
		t.Fatalf("github received secret %q, want 32 random bytes in hex", f.gh.secrets)
	}
	secret := f.gh.secrets[0]
	if f.gh.sawSecretInArgv(secret) {
		t.Error("the secret reached gh through a command line, where every process can read it")
	}
	if strings.Contains(out, secret) {
		t.Errorf("connect printed the secret:\n%s", out)
	}

	bound, err := f.store.GetGitHubWebhookBinding(context.Background(), "build", f.repo)
	if err != nil {
		t.Fatalf("the controller stored no binding: %v", err)
	}
	if bound.Secret != secret {
		t.Error("the controller and github hold different secrets")
	}
	if bound.HookID != hook.ID {
		t.Errorf("binding hook id = %d, want %d", bound.HookID, hook.ID)
	}

	for _, want := range []string{
		wantURL, "push, pull_request", "ping:", "200",
		fmt.Sprintf("%d (created)", hook.ID),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("connect output is missing %q:\n%s", want, out)
		}
	}
}

func TestWebhooksConnect_ReconnectRotatesTheSecretInPlace(t *testing.T) {
	f := newConnectFixture(t)
	for range 2 {
		captureStdout(t, func() {
			if err := runWebhooksConnect([]string{
				"--profile", "prod", "--repo", f.repo, "--pipeline", "build",
			}); err != nil {
				t.Fatalf("connect: %v", err)
			}
		})
	}
	if len(f.gh.hooks) != 1 {
		t.Fatalf("hooks = %d, want the second connect to update the first", len(f.gh.hooks))
	}
	if len(f.gh.secrets) < 2 || f.gh.secrets[0] == f.gh.secrets[len(f.gh.secrets)-1] {
		t.Fatal("reconnecting did not rotate the secret")
	}
	bound, err := f.store.GetGitHubWebhookBinding(context.Background(), "build", f.repo)
	if err != nil {
		t.Fatalf("GetGitHubWebhookBinding: %v", err)
	}
	if bound.Secret != f.gh.secrets[len(f.gh.secrets)-1] {
		t.Error("the controller kept a secret github no longer signs with")
	}
}

func TestWebhooksConnect_ReportsAControllerThatRefusedThePing(t *testing.T) {
	f := newConnectFixture(t)
	f.gh.pingStatus = 401
	var err error
	out := captureStdout(t, func() {
		err = runWebhooksConnect([]string{
			"--profile", "prod", "--repo", f.repo, "--pipeline", "build",
		})
	})
	if err == nil {
		t.Fatal("connect reported success for a ping the controller refused")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error = %v, want the status the controller answered", err)
	}
	if !strings.Contains(out, "unverified") {
		t.Errorf("output does not say the ping was unverified:\n%s", out)
	}
}

func TestWebhooksDisconnect_RemovesBothSides(t *testing.T) {
	f := newConnectFixture(t)
	captureStdout(t, func() {
		if err := runWebhooksConnect([]string{
			"--profile", "prod", "--repo", f.repo, "--pipeline", "build",
		}); err != nil {
			t.Fatalf("connect: %v", err)
		}
	})

	var err error
	out := captureStdout(t, func() {
		err = runWebhooksDisconnect([]string{
			"--profile", "prod", "--repo", f.repo, "--pipeline", "build",
		})
	})
	if err != nil {
		t.Fatalf("disconnect: %v\n%s", err, out)
	}
	if len(f.gh.hooks) != 0 {
		t.Errorf("hooks after disconnect = %+v, want none", f.gh.hooks)
	}
	if _, err := f.store.GetGitHubWebhookBinding(context.Background(), "build", f.repo); err == nil {
		t.Error("the controller kept the binding after a disconnect")
	}
	for _, want := range []string{"deleted", "binding: removed"} {
		if !strings.Contains(out, want) {
			t.Errorf("disconnect output is missing %q:\n%s", want, out)
		}
	}
}

// A repository connected to two controllers under one pipeline name loses
// only the webhook the controller being disconnected was bound to.
func TestWebhooksDisconnect_LeavesAnotherControllersHook(t *testing.T) {
	f := newConnectFixture(t)
	other := githubHook{
		ID:     91,
		Active: true,
		Config: githubHookConfig{URL: "https://staging.example.dev/webhooks/github/build", ContentType: "json"},
	}
	f.gh.hooks = append(f.gh.hooks, other)
	captureStdout(t, func() {
		if err := runWebhooksConnect([]string{
			"--profile", "prod", "--repo", f.repo, "--pipeline", "build",
		}); err != nil {
			t.Fatalf("connect: %v", err)
		}
	})
	if len(f.gh.hooks) != 2 {
		t.Fatalf("hooks after connect = %+v, want the other controller's kept", f.gh.hooks)
	}
	out := captureStdout(t, func() {
		if err := runWebhooksDisconnect([]string{
			"--profile", "prod", "--repo", f.repo, "--pipeline", "build",
		}); err != nil {
			t.Fatalf("disconnect: %v", err)
		}
	})
	if len(f.gh.hooks) != 1 || f.gh.hooks[0].ID != other.ID {
		t.Fatalf("hooks after disconnect = %+v, want only the other controller's", f.gh.hooks)
	}
	if strings.Contains(out, other.Config.URL) {
		t.Errorf("disconnect touched the other controller's hook:\n%s", out)
	}
}

func TestWebhooksDisconnect_ToleratesEitherSideBeingAbsent(t *testing.T) {
	f := newConnectFixture(t)
	var err error
	out := captureStdout(t, func() {
		err = runWebhooksDisconnect([]string{
			"--profile", "prod", "--repo", f.repo, "--pipeline", "build",
		})
	})
	if err != nil {
		t.Fatalf("disconnect of an unconnected repository: %v", err)
	}
	for _, want := range []string{"none pointed at that pipeline", "the controller held none"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
}

func TestWebhooksConnect_RequiresItsInputs(t *testing.T) {
	for name, args := range map[string][]string{
		"no repo":     {"--profile", "prod", "--pipeline", "build"},
		"no pipeline": {"--profile", "prod", "--repo", "acme/widgets"},
		"no profile":  {"--repo", "acme/widgets", "--pipeline", "build"},
		"no events":   {"--profile", "prod", "--repo", "acme/widgets", "--pipeline", "build", "--events", " , "},
	} {
		t.Run(name, func(t *testing.T) {
			err := runWebhooksConnect(args)
			if err == nil {
				t.Fatal("connect accepted an incomplete invocation")
			}
			if !strings.Contains(err.Error(), "webhooks connect:") {
				t.Errorf("error = %v, want it to name the command", err)
			}
		})
	}
}

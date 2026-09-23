package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func postBinding(t *testing.T, base, token string, req controller.GitHubWebhookBindingRequest) int {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal binding: %v", err)
	}
	httpReq, err := http.NewRequest(http.MethodPost, base+"/api/v1/webhooks/github/bindings", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build binding request: %v", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("post binding: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// A delivery runs in the team whose binding secret signed it, so two teams
// connecting one repository to one pipeline each receive only their own.
func TestGitHubWebhookBinding_ADeliveryRunsInTheTeamWhoseSecretSignedIt(t *testing.T) {
	st := openSQLiteBindingStore(t)
	f := newBindingFixture(t, st, nil)
	tenantB := teamTenant(t, st, teamB)

	const secretB = "team-b-webhook-secret-fixture"
	if err := tenantB.PutGitHubWebhookBinding(context.Background(), store.GitHubWebhookBinding{
		Pipeline: "build", Repo: "acme/widgets", Secret: secretB,
	}); err != nil {
		t.Fatalf("team B binding: %v", err)
	}
	f.connect(t, controller.GitHubWebhookBindingRequest{
		Pipeline: "build", Repo: "acme/widgets", Secret: bindingSecret,
	})

	ctx := context.Background()
	if _, err := tenantB.GetGitHubWebhookBinding(ctx, "build", "acme/widgets"); err != nil {
		t.Fatalf("team B's binding is not in team B: %v", err)
	}
	tenantDefault, err := st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatalf("ForTeam default: %v", err)
	}

	url := f.server.URL + "/webhooks/github/build"
	bodyB := pushBody("acme/widgets", "bbb111")
	respB := postWebhookDelivery(t, url, "push", "delivery-b", bodyB, signWebhook(secretB, bodyB))
	defer func() { _ = respB.Body.Close() }()
	if respB.StatusCode != http.StatusAccepted {
		t.Fatalf("delivery signed with team B's secret = %d, want 202", respB.StatusCode)
	}
	if _, err := tenantB.FindTriggerByWebhookReplay(ctx, "", "delivery-b"); err != nil {
		t.Errorf("team B's delivery is not a team B trigger: %v", err)
	}
	if _, err := tenantDefault.FindTriggerByWebhookReplay(ctx, "", "delivery-b"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("team B's delivery reads in the default team: %v", err)
	}

	bodyA := pushBody("acme/widgets", "aaa111")
	respA := postWebhookDelivery(t, url, "push", "delivery-a", bodyA, signWebhook(bindingSecret, bodyA))
	defer func() { _ = respA.Body.Close() }()
	if respA.StatusCode != http.StatusAccepted {
		t.Fatalf("delivery signed with the default team's secret = %d, want 202", respA.StatusCode)
	}
	if _, err := tenantDefault.FindTriggerByWebhookReplay(ctx, "", "delivery-a"); err != nil {
		t.Errorf("the default team's delivery is not a default trigger: %v", err)
	}
	if _, err := tenantB.FindTriggerByWebhookReplay(ctx, "", "delivery-a"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the default team's delivery reads in team B: %v", err)
	}

	// safety: the delivery id is an unsigned header, so a default-team delivery
	// reusing team B's is refused without naming team B's run.
	replay := pushBody("acme/widgets", "aaa222")
	dup := postWebhookDelivery(t, url, "push", "delivery-b", replay, signWebhook(bindingSecret, replay))
	defer func() { _ = dup.Body.Close() }()
	raw, _ := io.ReadAll(dup.Body)
	if dup.StatusCode != http.StatusConflict {
		t.Errorf("a reused delivery id = %d, want 409", dup.StatusCode)
	}
	if bytes.Contains(raw, []byte(`"run_id"`)) {
		t.Errorf("a reused delivery id answered with another team's run: %s", raw)
	}

	bad := postWebhookDelivery(t, url, "push", "delivery-c", bodyB, signWebhook("neither-team", bodyB))
	defer func() { _ = bad.Body.Close() }()
	if bad.StatusCode != http.StatusUnauthorized {
		t.Errorf("delivery signed by neither team = %d, want 401", bad.StatusCode)
	}
}

// Nothing proves a team controls the repository it names, and a binding makes
// that team's secret sign for the repository, so connecting one is the
// operator's alone.
func TestGitHubWebhookBinding_ATeamAdminCannotConnectARepository(t *testing.T) {
	st := openSQLiteBindingStore(t)
	f := newBindingFixture(t, st, nil)
	tenantB := teamTenant(t, st, teamB)
	adminB := teamToken(t, tenantB, controller.ScopeTeamAdmin)

	if got := postBinding(t, f.server.URL, adminB, controller.GitHubWebhookBindingRequest{
		Pipeline: "build", Repo: "victim/app", Secret: "attacker-chosen",
	}); got != http.StatusForbidden {
		t.Fatalf("team admin connect = %d, want 403", got)
	}
	req, err := http.NewRequest(http.MethodDelete,
		f.server.URL+"/api/v1/webhooks/github/bindings?pipeline=build&repo=victim/app", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+adminB)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("team admin disconnect = %d, want 403", resp.StatusCode)
	}
	bindings, err := tenantB.ListGitHubWebhookBindings(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 0 {
		t.Fatalf("team B holds bindings %+v", bindings)
	}
}

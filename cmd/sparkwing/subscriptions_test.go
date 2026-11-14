package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

type subscriptionManager struct {
	input   controllerwire.SubscriptionWrite
	id      string
	missing bool
}

var fixtureSubscription = controllerwire.Subscription{ID: "sub_1", URL: "https://hooks.example/a", EventTypes: []string{"run.finished"}, Statuses: []string{"failed"}, Pipelines: []string{}, Repositories: []string{}}

func (m *subscriptionManager) ListSubscriptions(context.Context) ([]controllerwire.Subscription, error) {
	return []controllerwire.Subscription{fixtureSubscription}, nil
}

func (m *subscriptionManager) GetSubscription(_ context.Context, id string) (controllerwire.Subscription, error) {
	m.id = id
	if m.missing {
		return controllerwire.Subscription{}, &apierr.Error{Code: apierr.NotFound, Message: "controller GET /api/v1/subscriptions/" + id + ": resource not found"}
	}
	return fixtureSubscription, nil
}

func (m *subscriptionManager) CreateSubscription(_ context.Context, input controllerwire.SubscriptionWrite) (controllerwire.SubscriptionSecret, error) {
	m.input = input
	return controllerwire.SubscriptionSecret{Subscription: fixtureSubscription, Secret: "whsec_new"}, nil
}

func (m *subscriptionManager) DeleteSubscription(_ context.Context, id string) (controllerwire.SubscriptionDeletion, error) {
	m.id = id
	return controllerwire.SubscriptionDeletion{ID: id, Deleted: !m.missing}, nil
}

func (m *subscriptionManager) RotateSubscriptionSecret(_ context.Context, id string) (controllerwire.SubscriptionSecret, error) {
	m.id = id
	return controllerwire.SubscriptionSecret{Subscription: fixtureSubscription, Secret: "whsec_rotated"}, nil
}

func runSubscriptionCommand(t *testing.T, m *subscriptionManager, args ...string) (string, string) {
	t.Helper()
	inv, err := clisurface.Parse("sparkwing", append([]string{"subscriptions"}, args...))
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := manageSubscriptions(t.Context(), inv, m, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	return out.String(), errOut.String()
}

func TestSubscriptionsCreatePassesFiltersAndPrintsTheSecretOnce(t *testing.T) {
	m := &subscriptionManager{}
	out, errOut := runSubscriptionCommand(t, m, "create", "--url", "https://hooks.example/a", "--event", "run.finished", "--status", "failed,cancelled", "--pipeline", "test", "--repo", "gh:1", "--description", "pager", "-o", "pretty")
	want := controllerwire.SubscriptionWrite{URL: "https://hooks.example/a", EventTypes: []string{"run.finished"}, Statuses: []string{"failed", "cancelled"}, Pipelines: []string{"test"}, Repositories: []string{"gh:1"}, Description: "pager"}
	got := m.input
	if got.IdempotencyKey == "" {
		t.Fatal("create sent no idempotency key")
	}
	got.IdempotencyKey = ""
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("create sent %+v", got)
	}
	if strings.Count(out, "whsec_new") != 1 || !strings.Contains(errOut, "will not show it again") {
		t.Fatalf("pretty create: %q %q", out, errOut)
	}
	if out, _ := runSubscriptionCommand(t, m, "create", "--url", "https://hooks.example/a", "-o", "plain"); out != "sub_1\nwhsec_new\n" {
		t.Fatalf("plain create: %q", out)
	}
	out, _ = runSubscriptionCommand(t, m, "create", "--url", "https://hooks.example/a", "-o", "json")
	var secret controllerwire.SubscriptionSecret
	if err := json.Unmarshal([]byte(out), &secret); err != nil || secret.Secret != "whsec_new" || secret.Subscription.ID != "sub_1" {
		t.Fatalf("json create: %q %v", out, err)
	}
}

func TestSubscriptionsReadsNeverPrintASecret(t *testing.T) {
	m := &subscriptionManager{}
	for _, args := range [][]string{{"list", "-o", "json"}, {"list", "-o", "plain"}, {"list", "-o", "pretty"}, {"show", "--subscription", "sub_1", "-o", "json"}, {"show", "--subscription", "sub_1", "-o", "pretty"}} {
		out, _ := runSubscriptionCommand(t, m, args...)
		if strings.Contains(out, "whsec_") || !strings.Contains(out, "sub_1") && args[0] != "show" {
			t.Fatalf("%v printed %q", args, out)
		}
	}
	if out, _ := runSubscriptionCommand(t, m, "list", "-o", "plain"); out != "sub_1\n" {
		t.Fatalf("plain list: %q", out)
	}
	if out, _ := runSubscriptionCommand(t, m, "show", "--subscription", "sub_1", "-o", "plain"); out != "https://hooks.example/a\n" || m.id != "sub_1" {
		t.Fatalf("plain show: %q", out)
	}
}

func TestSubscriptionsRotateAndDelete(t *testing.T) {
	m := &subscriptionManager{}
	if out, _ := runSubscriptionCommand(t, m, "rotate-secret", "--subscription", "sub_1", "-o", "plain"); out != "whsec_rotated\n" || m.id != "sub_1" {
		t.Fatalf("plain rotate: %q", out)
	}
	if out, _ := runSubscriptionCommand(t, m, "delete", "--subscription", "sub_9", "-o", "json"); !strings.Contains(out, `"deleted":true`) || m.id != "sub_9" {
		t.Fatalf("json delete: %q", out)
	}
}

func TestSubscriptionsReportAMissingSubscriptionAndTheNextCommand(t *testing.T) {
	m := &subscriptionManager{missing: true}
	for _, args := range [][]string{
		{"delete", "--subscription", "sub_9", "-o", "pretty"}, {"delete", "--subscription", "sub_9", "-o", "plain"},
		{"show", "--subscription", "sub_9", "-o", "json"},
	} {
		inv, err := clisurface.Parse("sparkwing", append([]string{"subscriptions"}, args...))
		if err != nil {
			t.Fatal(err)
		}
		var out, errOut bytes.Buffer
		err = manageSubscriptions(t.Context(), inv, m, &out, &errOut)
		var failure *apierr.Error
		if !errors.As(err, &failure) || failure.Code != apierr.NotFound || !strings.Contains(failure.Message, "sub_9 was not found") || !strings.Contains(failure.Hint, "sparkwing subscriptions list") || out.Len() != 0 {
			t.Fatalf("%v: err=%v out=%q", args, err, out.String())
		}
	}
	if out, _ := runSubscriptionCommand(t, m, "delete", "--subscription", "sub_9", "-o", "json"); !strings.Contains(out, `"deleted":false`) {
		t.Fatalf("json delete of a missing subscription: %q", out)
	}
}

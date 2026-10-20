package controller_test

import (
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type teamStanding struct {
	Standing controllerwire.TeamStanding `json:"standing"`
}

func TestStandingRequestFromBillingToOperatorDecision(t *testing.T) {
	f, _ := billingFixture(t)
	owner, editor, _ := teamOf(f)
	operator := f.user("op", "korey@example.com")
	f.srv.WithOperatorAccounts([]string{operator.id})
	controller := "/api/v1/team/billing/standing-request"
	queue := "/api/v1/operator/standing-requests"
	decline := "/api/v1/operator/teams/" + owner.team + "/standing-request/decline"
	ask := map[string]any{"tier": "high", "message": "Release builds need 256 vCPU."}

	var before teamStanding
	if code := f.call("GET", "/api/v1/team/billing", owner.auth, nil, &before); code != http.StatusOK || before.Standing.Tier != "New" || before.Standing.History.Tier != "New" || before.Standing.PrepayCeiling != "Established" || !before.Standing.CanRequest || before.Standing.Request != nil {
		t.Fatalf("standing before a request = %d %+v", code, before)
	}
	for _, tc := range []struct {
		name string
		auth string
		body map[string]any
		want int
	}{
		{"an editor", editor.auth, ask, http.StatusForbidden},
		{"anonymous", "", ask, http.StatusUnauthorized},
		{"an unknown tier", owner.auth, map[string]any{"tier": "ultra", "message": "m"}, http.StatusBadRequest},
		{"no message", owner.auth, map[string]any{"tier": "high", "message": " "}, http.StatusBadRequest},
	} {
		if code := f.call("POST", controller, tc.auth, tc.body, nil); code != tc.want {
			t.Errorf("%s = %d want %d", tc.name, code, tc.want)
		}
	}
	var made controllerwire.StandingRequest
	if code := f.call("POST", controller, owner.auth, ask, &made); code != http.StatusCreated || made.State != "open" || made.Tier != "High" || made.RequestedBy != "olga@example.com" || made.Team != owner.team {
		t.Fatalf("owner request = %d %+v", code, made)
	}
	if code := f.call("POST", controller, owner.auth, ask, nil); code != http.StatusConflict {
		t.Errorf("a second open request = %d want 409", code)
	}

	for _, auth := range []string{owner.auth, "Bearer " + f.admin} {
		if code := f.call("GET", queue, auth, nil, nil); code != http.StatusForbidden {
			t.Errorf("queue read by a non-operator = %d want 403", code)
		}
		if code := f.call("POST", decline, auth, map[string]any{"request": made.ID, "reason": "no"}, nil); code != http.StatusForbidden {
			t.Errorf("decline by a non-operator = %d want 403", code)
		}
	}
	var listed controllerwire.OperatorStandingQueue
	if code := f.call("GET", queue, operator.auth, nil, &listed); code != http.StatusOK || len(listed.Requests) != 1 {
		t.Fatalf("queue = %d %+v", code, listed)
	}
	item := listed.Requests[0]
	if item.Request.ID != made.ID || item.Request.Message != "Release builds need 256 vCPU." || item.Standing != "New" || item.History.Tier != "New" || item.PrepayCeiling != "Established" || item.Request.RequestedBy != "olga@example.com" {
		t.Errorf("queue item = %+v", item)
	}

	if code := f.call("POST", decline, operator.auth, map[string]any{"request": made.ID, "reason": " "}, nil); code != http.StatusBadRequest {
		t.Errorf("decline without a reason = %d want 400", code)
	}
	if code := f.call("POST", decline, operator.auth, map[string]any{"reason": "no request"}, nil); code != http.StatusBadRequest {
		t.Errorf("decline naming no request = %d want 400", code)
	}
	if code := f.call("POST", decline, operator.auth, map[string]any{"request": made.ID, "reason": "Pay for 30 days first."}, nil); code != http.StatusOK {
		t.Fatalf("decline = %d", code)
	}
	if code := f.call("POST", decline, operator.auth, map[string]any{"request": made.ID, "reason": "again"}, nil); code != http.StatusConflict {
		t.Errorf("retried decline = %d want 409", code)
	}
	var declined teamStanding
	if code := f.call("GET", "/api/v1/team/billing", editor.auth, nil, &declined); code != http.StatusOK || declined.Standing.Request == nil || declined.Standing.Request.State != "declined" || declined.Standing.Request.DecisionReason != "Pay for 30 days first." || declined.Standing.Request.DecidedAt == 0 || declined.Standing.CanRequest {
		t.Fatalf("requester view after decline = %d %+v", code, declined.Standing)
	}

	declinedID := made.ID
	if code := f.call("POST", controller, owner.auth, ask, &made); code != http.StatusCreated {
		t.Fatalf("request after decline = %d", code)
	}
	trust := "/api/v1/operator/teams/" + owner.team + "/trust"
	if code := f.call("POST", trust, operator.auth, map[string]any{"trust": "granted", "reason": "stale", "standing_request": declinedID}, nil); code != http.StatusConflict {
		t.Fatalf("grant naming a decided request = %d want 409", code)
	}
	if code := f.call("POST", trust, operator.auth, map[string]any{"trust": "granted", "reason": "Known customer", "standing_request": made.ID}, nil); code != http.StatusOK {
		t.Fatalf("grant = %d", code)
	}
	var approved teamStanding
	if code := f.call("GET", "/api/v1/team/billing", owner.auth, nil, &approved); code != http.StatusOK || approved.Standing.Tier != "High" || approved.Standing.Request == nil || approved.Standing.Request.ID != made.ID || approved.Standing.Request.State != "approved" || !approved.Standing.CanRequest {
		t.Fatalf("requester view after the grant = %d %+v", code, approved.Standing)
	}
	if code := f.call("GET", queue, operator.auth, nil, &listed); code != http.StatusOK || len(listed.Requests) != 0 {
		t.Errorf("queue after the grant = %d %+v", code, listed)
	}

	var team operatorTeam
	if code := f.call("GET", "/api/v1/operator/teams/"+owner.team, operator.auth, nil, &team); code != http.StatusOK {
		t.Fatalf("operator team read = %d", code)
	}
	counts := map[string]int{}
	for _, ev := range team.Events {
		counts[ev.Kind]++
	}
	if counts[store.BusinessEventStandingRequested] != 2 || counts[store.BusinessEventStandingRequestDecided] != 2 || counts[store.BusinessEventBillingTrustChanged] != 1 {
		t.Errorf("audit = %v", counts)
	}
}

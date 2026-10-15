package controller_test

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/license/licensetest"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

// safety: the API never mints credits.grant, so the checkout service's token
// is seeded the way the controller seeds it from its credits-token credential.
func (f *identityFixture) creditsGrantToken() string {
	f.t.Helper()
	raw := "sws_" + rand.Text() + rand.Text()
	if created, err := controller.EnsureCheckoutToken(context.Background(), f.store, raw, time.Now()); err != nil || !created {
		f.t.Fatalf("seed the checkout token = %v, %v", created, err)
	}
	return "Bearer " + raw
}

var muxRoute = regexp.MustCompile(`mux\.Handle\("([A-Z]+) ([^"]+)"`)

// The checkout service's credential records a paid grant, reverses a payment,
// holds the team a payment funded and reads the ledger's units, and every
// other route refuses it.
func TestCreditsGrantScopeReachesOnlyTheGrantRoutes(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("o", "olga@example.com")
	grant := f.creditsGrantToken()
	cent := int64(store.MicroCreditsPerCent)

	if code := f.call("POST", "/api/v1/credits/grants", grant, map[string]any{
		"kind": "paid", "amount_micro": 500 * cent, "reference": "pi_1", "team": owner.team,
	}, nil); code != http.StatusCreated {
		t.Fatalf("a paid grant = %d want 201", code)
	}
	for _, body := range []map[string]any{
		{"kind": "free", "amount_micro": 500 * cent, "team": owner.team},
		{"kind": "reversal", "amount_micro": -100 * cent, "reference": "re_1", "reverses": "pi_1"},
	} {
		if code := f.call("POST", "/api/v1/credits/grants", grant, body, nil); code != http.StatusBadRequest {
			t.Errorf("a %s grant with credits.grant = %d want 400", body["kind"], code)
		}
	}
	if code := f.call("POST", "/api/v1/credits/freezes", grant,
		map[string]any{"team": owner.team, "dispute_id": "dp_x"}, nil); code != http.StatusBadRequest {
		t.Errorf("freezing a named team with credits.grant = %d want 400", code)
	}
	if code := f.call("POST", "/api/v1/credits/freezes", grant,
		map[string]any{"payment_id": "pi_1", "release": true}, nil); code != http.StatusBadRequest {
		t.Errorf("releasing with credits.grant = %d want 400", code)
	}
	if code := f.call("GET", "/api/v1/credits/units", grant, nil, nil); code != http.StatusOK {
		t.Errorf("units = %d want 200", code)
	}

	allowed := map[string]bool{
		"POST /api/v1/credits/grants":              true,
		"POST /api/v1/credits/reversals":           true,
		"POST /api/v1/credits/freezes":             true,
		"GET /api/v1/credits/units":                true,
		"GET /api/v1/credits/payments/{reference}": true,
		"POST /api/v1/credits/cards":               true,
		"POST /api/v1/credits/card-payments":       true,
		"POST /api/v1/credits/warnings":            true,
		"POST /api/v1/credits/card-refunds":        true,
		// safety: both answer any authenticated caller, the first with its own
		// identity and the second with the service URLs.
		"GET /api/v1/auth/whoami": true,
		"GET /api/v1/services":    true,
	}
	now := time.Now()
	if err := f.store.CreateTriggerWithRun(context.Background(),
		store.Trigger{ID: "rx", Pipeline: "build", CreatedAt: now},
		store.Run{ID: "rx", Pipeline: "build", Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, m := range muxRoute.FindAllStringSubmatch(string(src), -1) {
		route := m[1] + " " + m[2]
		if allowed[route] {
			continue
		}
		path := strings.ReplaceAll(m[2], "{id}", "rx")
		path = regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(path, "x")
		path = strings.TrimSuffix(path, "...")
		code := f.call(m[1], path, grant, map[string]any{}, nil)
		if code != http.StatusForbidden && code != http.StatusUnauthorized {
			t.Errorf("%s with a credits.grant token = %d, want 401 or 403", route, code)
		}
		checked++
	}
	if checked < 50 {
		t.Fatalf("checked %d routes; the route pattern no longer matches server.go", checked)
	}
}

// The payment lookup answers the team a paid grant funded, or 404 with a code
// that tells a payment no grant carries apart from a route the controller does
// not serve, and it writes nothing.
func TestPaidGrantLookupAnswersThePayingTeam(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("o", "olga@example.com")
	grant := f.creditsGrantToken()
	if code := f.call("POST", "/api/v1/credits/grants", grant, map[string]any{
		"kind": "paid", "amount_micro": 500 * int64(store.MicroCreditsPerCent), "reference": "pi_1", "team": owner.team,
	}, nil); code != http.StatusCreated {
		t.Fatalf("grant = %d", code)
	}
	var before teamBilling
	f.call("GET", "/api/v1/team/billing", owner.auth, nil, &before)
	var found struct {
		Team string `json:"team"`
	}
	if code := f.call("GET", "/api/v1/credits/payments/pi_1", grant, nil, &found); code != http.StatusOK || found.Team != owner.team {
		t.Fatalf("lookup of a granted payment = %d %+v", code, found)
	}
	var missing struct {
		Code string `json:"code"`
	}
	if code := f.call("GET", "/api/v1/credits/payments/pi_unknown", grant, nil, &missing); code != http.StatusNotFound ||
		missing.Code != controller.UnknownPaymentCode {
		t.Fatalf("lookup of an unknown payment = %d %+v", code, missing)
	}
	if code := f.call("GET", "/api/v1/credits/payments/pi_1", owner.auth, nil, nil); code != http.StatusForbidden {
		t.Errorf("an owner's lookup = %d, want 403", code)
	}
	var unserved struct {
		Code string `json:"code"`
	}
	if code := f.call("GET", "/api/v1/credits/no-such-route/pi_1", grant, nil, &unserved); code != http.StatusNotFound ||
		unserved.Code == controller.UnknownPaymentCode {
		t.Errorf("an unserved route = %d %+v, want 404 without the unknown-payment code", code, unserved)
	}
	var after teamBilling
	f.call("GET", "/api/v1/team/billing", owner.auth, nil, &after)
	if after.BalanceMicro != before.BalanceMicro {
		t.Errorf("balance moved from %d to %d across lookups", before.BalanceMicro, after.BalanceMicro)
	}
}

// No API caller mints or rotates the checkout service's scope: not a team
// owner, and not the admin token, whose mint would restore every operator
// power the console holds.
func TestCreditsGrantScopeIsNeverMintedThroughTheAPI(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("o", "olga@example.com")
	admin := "Bearer " + f.admin
	if code := f.call("POST", "/api/v1/team/tokens", owner.auth, map[string]any{
		"name": "sneaky", "scopes": []string{controller.ScopeCreditsGrant},
	}, nil); code < 400 {
		t.Errorf("a team token carrying credits.grant = %d, want a refusal", code)
	}
	for _, who := range []struct{ name, auth string }{{"an owner", owner.auth}, {"the admin token", admin}} {
		for _, kind := range []string{"service", "user", "runner"} {
			if code := f.call("POST", "/api/v1/tokens", who.auth, map[string]any{
				"principal": "x", "kind": kind, "scopes": []string{controller.ScopeCreditsGrant},
			}, nil); code != http.StatusForbidden {
				t.Errorf("%s minting a %s token carrying credits.grant = %d, want 403", who.name, kind, code)
			}
		}
	}

	grant := f.creditsGrantToken()
	prefix := strings.TrimPrefix(grant, "Bearer ")[:store.PrefixLen]
	var rotated struct {
		Token string `json:"token"`
	}
	if code := f.call("POST", "/api/v1/tokens/"+prefix+"/rotate", admin, map[string]any{}, &rotated); code != http.StatusForbidden || rotated.Token != "" {
		t.Errorf("the admin token rotating the checkout token = %d %q, want 403 and no token", code, rotated.Token)
	}
	if code := f.call("GET", "/api/v1/credits/units", grant, nil, nil); code != http.StatusOK {
		t.Errorf("the checkout token after a refused rotation = %d, want 200", code)
	}

	user, _, err := f.store.CreateToken("legacy", store.TokenKindUser, []string{controller.ScopeCreditsGrant}, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if code := f.call("GET", "/api/v1/credits/units", "Bearer "+user, nil, nil); code != http.StatusForbidden {
		t.Errorf("a user token carrying credits.grant = %d, want 403: only a service token passes", code)
	}
}

// The credits-token credential seeds its token once: a restart with the same
// file writes nothing, and once the token is revoked the file is refused, even
// after pruning has deleted the revoked row.
func TestCheckoutTokenSeedsOnceAndNeverRevives(t *testing.T) {
	f := newIdentityFixture(t)
	ctx := context.Background()
	raw := "sws_" + rand.Text() + rand.Text()
	now := time.Now()
	for i, want := range []bool{true, false} {
		if created, err := controller.EnsureCheckoutToken(ctx, f.store, raw, now); err != nil || created != want {
			t.Fatalf("seed #%d = %v, %v; want %v", i+1, created, err, want)
		}
	}
	if created, err := controller.EnsureCheckoutToken(ctx, f.store, "", now); err != nil || created {
		t.Fatalf("an empty credential = %v, %v; want nothing written", created, err)
	}
	if code := f.call("DELETE", "/api/v1/tokens/"+raw[:store.PrefixLen], "Bearer "+f.admin, nil, nil); code >= 300 {
		t.Fatalf("revoke = %d", code)
	}
	if _, err := controller.EnsureCheckoutToken(ctx, f.store, raw, now); !errors.Is(err, store.ErrSeededTokenRevoked) {
		t.Fatalf("reseeding a revoked token = %v; want ErrSeededTokenRevoked", err)
	}
	later := now.Add(store.SpentIdentityRetention + 24*time.Hour)
	pruned, err := f.store.PruneSpentIdentity(ctx, later)
	if err != nil || pruned.Tokens == 0 {
		t.Fatalf("prune = %+v, %v; want the revoked token deleted", pruned, err)
	}
	if _, err := f.store.LookupTokenByPrefix(raw[:store.PrefixLen]); err == nil {
		t.Fatal("the revoked row survived pruning, so the test proves nothing")
	}
	if created, err := controller.EnsureCheckoutToken(ctx, f.store, raw, later); created || !errors.Is(err, store.ErrSeededTokenRevoked) {
		t.Fatalf("the restart after pruning = %v, %v; want the credential refused, not revived", created, err)
	}
	if code := f.call("GET", "/api/v1/credits/units", "Bearer "+raw, nil, nil); code != http.StatusUnauthorized {
		t.Errorf("the revoked checkout token after a restart = %d, want 401", code)
	}

	minted, _, err := f.store.CreateToken("old-checkout", store.TokenKindService, []string{controller.ScopeCreditsGrant}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if created, err := controller.EnsureCheckoutToken(ctx, f.store, minted, now); err != nil || created {
		t.Fatalf("adopting a token minted before provisioning = %v, %v; want it kept, nothing written", created, err)
	}
}

// The token routes normalize the scopes they are handed once, so a padded or
// re-cased spelling of the checkout scope is refused like the plain one.
func TestCreateTokenRefusesEverySpellingOfTheCheckoutScope(t *testing.T) {
	f := newIdentityFixture(t)
	admin := "Bearer " + f.admin
	for scope, want := range map[string]int{
		"credits.grant":     http.StatusForbidden,
		" credits.grant ":   http.StatusForbidden,
		"credits.grant\t":   http.StatusForbidden,
		"\tcredits.grant\n": http.StatusForbidden,
		"Credits.Grant":     http.StatusBadRequest,
		"CREDITS.GRANT":     http.StatusBadRequest,
	} {
		var minted struct {
			Token string `json:"token"`
		}
		code := f.call("POST", "/api/v1/tokens", admin, map[string]any{
			"principal": "checkout", "kind": "service", "scopes": []string{"runs.read", scope},
		}, &minted)
		if code != want || minted.Token != "" {
			t.Errorf("minting %q = %d, token %q; want %d and none", scope, code, minted.Token, want)
		}
	}
	var minted struct {
		Metadata struct {
			Scopes []string `json:"scopes"`
		} `json:"metadata"`
	}
	if code := f.call("POST", "/api/v1/tokens", admin, map[string]any{
		"principal": "reader", "kind": "user", "scopes": []string{" runs.read ", "runs.read", ""},
	}, &minted); code != http.StatusCreated || !slices.Equal(minted.Metadata.Scopes, []string{"runs.read"}) {
		t.Errorf("a padded known scope = %d %v, want 201 storing [runs.read]", code, minted.Metadata.Scopes)
	}
}

// A metering controller serving without credentials, one with an empty token
// table, still answers the checkout service's routes only to its token.
func TestServiceRoutesRefuseAnAnonymousCaller(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	pub, priv := licensetest.NewKey(t)
	now := time.Now()
	raw := licensetest.Sign(t, priv, licensetest.Terms{
		Features: []string{license.FeatureMetering}, IssuedTo: "test",
		IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(24 * time.Hour),
	})
	ctrl := controller.New(st, nil).EnableAuthFromStore().WithLicense(license.Resolve(raw, pub, now, nil))
	srv := httptest.NewServer(ctrl.Handler())
	t.Cleanup(srv.Close)
	for _, route := range [][2]string{
		{"POST", "/api/v1/credits/grants"},
		{"POST", "/api/v1/credits/reversals"},
		{"POST", "/api/v1/credits/freezes"},
		{"POST", "/api/v1/credits/warnings"},
		{"GET", "/api/v1/credits/units"},
	} {
		code, _ := creditsRequest(t, route[0], srv.URL+route[1], "", map[string]any{
			"kind": "paid", "amount_micro": 5, "reference": "pi_anon", "payment_id": "pi_anon", "dispute_id": "dp_anon",
		})
		if code != http.StatusUnauthorized {
			t.Errorf("anonymous %s %s = %d, want 401", route[0], route[1], code)
		}
	}
	if code, _ := creditsRequest(t, http.MethodGet, srv.URL+"/api/v1/credits", "", nil); code != http.StatusOK {
		t.Fatalf("the open controller's own reads = %d, want 200: the route check, not auth, refused", code)
	}
}

// A refund and a lost chargeback reverse what a payment still has, in the
// team it funded. A hold named by payment holds that team per dispute,
// replaying it changes nothing, and only the operator's console releases it.
func TestReversalsAndFreezesFollowThePayment(t *testing.T) {
	f := newIdentityFixture(t)
	owner := f.user("o", "olga@example.com")
	other := f.user("x", "xena@example.com")
	operator := f.user("op", "korey@example.com")
	f.srv.WithOperatorAccounts([]string{operator.id})
	grant := f.creditsGrantToken()
	cent := int64(store.MicroCreditsPerCent)
	if code := f.call("POST", "/api/v1/credits/grants", grant, map[string]any{
		"kind": "paid", "amount_micro": 400 * cent, "reference": "pi_1", "team": owner.team,
	}, nil); code != http.StatusCreated {
		t.Fatalf("grant = %d", code)
	}

	type freezeResp struct {
		Team     string   `json:"team"`
		Frozen   bool     `json:"frozen"`
		Disputes []string `json:"disputes"`
	}
	var frozen freezeResp
	for _, dispute := range []string{"dp_1", "dp_2", "dp_1"} {
		if code := f.call("POST", "/api/v1/credits/freezes", grant, map[string]any{
			"payment_id": "pi_1", "dispute_id": dispute, "reason": "dispute opened",
		}, &frozen); code != http.StatusOK || frozen.Team != owner.team || !frozen.Frozen {
			t.Fatalf("hold %s by payment = %d %+v", dispute, code, frozen)
		}
	}
	if len(frozen.Disputes) != 2 {
		t.Fatalf("holds = %v, want one per dispute", frozen.Disputes)
	}
	if code := f.call("POST", "/api/v1/credits/grants", grant, map[string]any{
		"kind": "paid", "amount_micro": 100 * cent, "reference": "pi_other", "team": other.team,
	}, nil); code != http.StatusCreated {
		t.Fatalf("other grant = %d", code)
	}
	var conflict struct {
		Code string `json:"code"`
	}
	if code := f.call("POST", "/api/v1/credits/freezes", grant, map[string]any{
		"payment_id": "pi_other", "dispute_id": "dp_1",
	}, &conflict); code != http.StatusConflict || conflict.Code != "dispute_conflict" {
		t.Fatalf("holding dp_1 for another team's payment = %d %+v, want 409 dispute_conflict", code, conflict)
	}
	if code := f.call("POST", "/api/v1/credits/reversals", grant, map[string]any{
		"payment_id": "pi_other", "reference": "dp_1",
	}, &conflict); code != http.StatusConflict || conflict.Code != "dispute_conflict" {
		t.Fatalf("reversing another payment under dp_1 = %d %+v, want 409 dispute_conflict", code, conflict)
	}
	var b, ob teamBilling
	f.call("GET", "/api/v1/team/billing", owner.auth, nil, &b)
	f.call("GET", "/api/v1/team/billing", other.auth, nil, &ob)
	if !b.Frozen || ob.Frozen {
		t.Errorf("frozen: payer %v, other %v; want only the payer held", b.Frozen, ob.Frozen)
	}

	var rev struct {
		Team          string `json:"team"`
		ReversedMicro int64  `json:"reversed_micro"`
		BalanceMicro  int64  `json:"balance_micro"`
		Created       bool   `json:"created"`
	}
	if code := f.call("POST", "/api/v1/credits/reversals", grant, map[string]any{
		"payment_id": "pi_1", "reference": "dp_1",
	}, &rev); code != http.StatusCreated || rev.Team != owner.team || rev.ReversedMicro != 400*cent || rev.BalanceMicro != 0 {
		t.Fatalf("reversal = %d %+v", code, rev)
	}
	if code := f.call("POST", "/api/v1/credits/reversals", grant, map[string]any{
		"payment_id": "pi_1", "reference": "dp_1",
	}, &rev); code != http.StatusOK || rev.Created {
		t.Errorf("a replayed reversal = %d %+v, want 200 with nothing written", code, rev)
	}
	if code := f.call("POST", "/api/v1/credits/reversals", grant, map[string]any{
		"payment_id": "pi_1", "reference": "refund:pi_1",
	}, &rev); code != http.StatusOK || rev.Created {
		t.Errorf("a refund of a payment already reversed = %d %+v, want 200 with nothing written", code, rev)
	}
	if code := f.call("POST", "/api/v1/credits/reversals", grant, map[string]any{
		"payment_id": "pi_unknown", "reference": "refund:pi_unknown",
	}, nil); code != http.StatusNotFound {
		t.Errorf("an unknown payment = %d want 404", code)
	}

	unfreeze := "/api/v1/operator/teams/" + owner.team + "/unfreeze"
	var held struct {
		Frozen bool     `json:"frozen"`
		Holds  []string `json:"holds"`
	}
	if code := f.call("POST", unfreeze, operator.auth, map[string]any{"hold": "dp_1", "reason": "dispute won"}, &held); code != http.StatusOK ||
		!held.Frozen || len(held.Holds) != 1 {
		t.Fatalf("release of one dispute = %d %+v, want dp_2 still holding the team", code, held)
	}
	if code := f.call("POST", unfreeze, operator.auth, map[string]any{"hold": "dp_2", "reason": "dispute won"}, &held); code != http.StatusOK || held.Frozen {
		t.Fatalf("release of the last dispute = %d %+v", code, held)
	}
	f.call("GET", "/api/v1/team/billing", owner.auth, nil, &b)
	if b.Frozen {
		t.Error("the team is still frozen after the release")
	}
}

// A reversal of a payment the ledger does not hold answers one 404 document
// and logs no reversal.
func TestAReversalOfAnUnknownPaymentAnswersOnly404(t *testing.T) {
	raw, pub := multiTeamLicense(t)
	logs := &lockedBuffer{}
	f := newIdentityFixtureWith(t, fixtureOpts{license: raw, key: pub, logger: slog.New(slog.NewJSONHandler(logs, nil))})
	var body map[string]any
	if code := f.call("POST", "/api/v1/credits/reversals", f.creditsGrantToken(), map[string]any{
		"payment_id": "pi_unknown", "reference": "refund:pi_unknown",
	}, &body); code != http.StatusNotFound || body["error"] == nil {
		t.Fatalf("unknown payment = %d %+v, want one 404 error document", code, body)
	}
	if strings.Contains(logs.String(), "payment reversed") {
		t.Fatalf("logs = %s; want no reversal logged", logs.String())
	}
}

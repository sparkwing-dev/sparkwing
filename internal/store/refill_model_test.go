package store_test

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

// The refill model test drives random histories of owner, operator, Stripe and controller events against the real
// store and checks them against a small model of what refills may do. SPARKWING_REFILL_MODEL_SEED adds one seed
// ("random" picks and logs one), and SPARKWING_REFILL_MODEL_HISTORIES sets histories per seed for a longer run.
const (
	refillModelSeedEnv      = "SPARKWING_REFILL_MODEL_SEED"
	refillModelHistoriesEnv = "SPARKWING_REFILL_MODEL_HISTORIES"
	refillModelSteps        = 40
	// refillModelKeyLife mirrors the store's 23-hour idempotency key life.
	refillModelKeyLife = 23 * time.Hour
	// A signed webhook later than the key life is the runbook case (a refund in Stripe), so the model delivers every
	// outcome before that.
	refillModelWebhookBound = 22 * time.Hour
)

type modelRefill struct {
	id, customer   string
	amount         int64
	opened, closed time.Time
	// failed is a failure outcome; cleared is a later Add funds lifting the stop it caused.
	failed, cleared bool
	// captured is Stripe's charge; answered is the controller recording its payment intent from the checkout
	// service's answer; credited is its signed payment granting credit.
	captured, answered, credited bool
	capturedAt                   time.Time
}

func (r *modelRefill) open() bool { return r.closed.IsZero() }

type modelTeam struct {
	name      store.Team
	tenant    *store.Tenant
	settings  store.RefillSettings
	hold      bool
	customer  string
	cardAdded time.Time
	cards     int
	balance   int64
	addFunds  []time.Time
	refills   map[string]*modelRefill
	openID    string
}

// blocked is the model's failed-refill stop: a failure on a refill opened since the current card was saved, with no
// Add funds paid after that failure or after that refill opened.
func (m *modelTeam) blocked() bool {
	for _, r := range m.refills {
		if !r.failed || r.cleared || r.opened.Before(m.cardAdded) {
			continue
		}
		if !slices.ContainsFunc(m.addFunds, func(a time.Time) bool { return a.After(r.opened) }) {
			return true
		}
	}
	return false
}

// spent is what refills other than own took or reserved since since: captured charges at their capture time and
// open, uncaptured reservations at their opening. Unless unseen is set, it leaves out a charge the store cannot see
// yet: a lost answer's, whose refill was closed (withdrawn or expired) before its signed payment arrived.
func (m *modelTeam) spent(own *modelRefill, since time.Time, unseen bool) int64 {
	var sum int64
	for _, r := range m.refills {
		at := r.opened
		switch {
		case r == own:
			continue
		case r.captured && !unseen && !r.answered && !r.credited && !r.open():
			continue
		case r.captured:
			at = r.capturedAt
		case !r.open():
			continue
		}
		if !at.Before(since) {
			sum += r.amount
		}
	}
	return sum
}

// eligible is the model's rule for charging own, or a new refill when own is nil, at now, counting every charge
// against the limits. A charge allowed only because a closed refill's charge has not been seen is reported as gap.
func (m *modelTeam) eligible(own *modelRefill, now time.Time) (ok bool, why string, gap bool) {
	if ok, why = m.eligibleCounting(own, now, true); ok {
		return true, "", false
	}
	if ok, _ = m.eligibleCounting(own, now, false); ok {
		return true, why, true
	}
	return false, why, false
}

func (m *modelTeam) eligibleCounting(own *modelRefill, now time.Time, unseen bool) (bool, string) {
	week, month := m.spent(own, now.Add(-7*24*time.Hour), unseen), m.spent(own, now.Add(-30*24*time.Hour), unseen)
	amount, customer := m.settings.AmountCents, m.customer
	if own != nil {
		amount, customer = own.amount, own.customer
	}
	switch {
	case !m.settings.Enabled:
		return false, "refills off"
	case m.hold:
		return false, "held"
	case m.blocked():
		return false, "blocked by a failed refill"
	case customer != m.customer:
		return false, "the refill's customer is no longer the team's"
	case m.balance >= m.settings.ThresholdCents*store.MicroCreditsPerCent:
		return false, fmt.Sprintf("balance %d not below threshold %d", m.balance, m.settings.ThresholdCents)
	case amount > store.MaxCardPaymentCents:
		return false, "over the payment maximum"
	case m.settings.WeekLimitCents > 0 && week+amount > m.settings.WeekLimitCents:
		return false, fmt.Sprintf("week limit %d with %d spent", m.settings.WeekLimitCents, week)
	case m.settings.MonthLimitCents > 0 && month+amount > m.settings.MonthLimitCents:
		return false, fmt.Sprintf("month limit %d with %d spent", m.settings.MonthLimitCents, month)
	}
	return true, ""
}

type modelWebhook struct {
	team   *modelTeam
	refill *modelRefill
	event  capability.PaymentEvent
}

type refillWorld struct {
	t       *testing.T
	s       *store.Store
	rng     *rand.Rand
	now     *time.Time
	prefix  string
	teams   []*modelTeam
	byName  map[store.Team]*modelTeam
	hasCard map[string]bool
	// outcomes is Stripe's idempotent result per refill id: "" paid, otherwise the decline code.
	outcomes map[string]string
	pending  []modelWebhook
	serial   int
	// touched names teams a concurrent event changed during the current pass.
	touched map[store.Team]bool
	// gaps counts charges allowed only because an earlier charge was not yet visible to the store.
	gaps  *int
	trace []string
}

var (
	errBeforeStripe = errors.New("checkout service unreachable")
	errAnswerLost   = errors.New("checkout answer lost")
	errNoCard       = errors.New("customer has no card")
)

func (w *refillWorld) fatalf(format string, args ...any) {
	w.t.Helper()
	tail := w.trace[max(len(w.trace)-30, 0):]
	w.t.Fatalf("%s\nlast events:\n  %s", fmt.Sprintf(format, args...), strings.Join(tail, "\n  "))
}

func (w *refillWorld) logf(format string, args ...any) {
	w.trace = append(w.trace, w.now.Format("01-02 15:04:05")+" "+fmt.Sprintf(format, args...))
}

func (w *refillWorld) id(kind string) string {
	w.serial++
	return fmt.Sprintf("%s_%s_%d", kind, w.prefix, w.serial)
}

func (w *refillWorld) randomSettings() store.RefillSettings {
	pick := func(values ...int64) int64 { return values[w.rng.IntN(len(values))] }
	return store.RefillSettings{
		Enabled:         w.rng.IntN(10) < 8,
		ThresholdCents:  pick(0, 1_500, 5_000, 20_000),
		AmountCents:     pick(1_000, 2_500, 5_000),
		WeekLimitCents:  pick(0, 0, 4_000, 10_000),
		MonthLimitCents: pick(0, 0, 8_000, 30_000),
	}
}

func (w *refillWorld) addTeam(name store.Team) {
	ctx := w.t.Context()
	if err := w.s.AsOperator().TestOnlyCreateTeam(ctx, name); err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.s.DB().ExecContext(ctx, storetest.Rebind(w.s, `UPDATE teams SET billing_trust='granted',billing_trust_at=1,billing_trust_by='operator',billing_trust_reason='customer' WHERE name=?`), string(name)); err != nil {
		w.t.Fatal(err)
	}
	tenant, err := w.s.ForTeam(ctx, name)
	if err != nil {
		w.t.Fatal(err)
	}
	m := &modelTeam{name: name, tenant: tenant, refills: map[string]*modelRefill{}}
	w.teams = append(w.teams, m)
	w.byName[name] = m
	w.addCard(m)
	w.setSettings(m, w.randomSettings())
}

// addCard completes a card setup, usually for the team's Stripe customer and sometimes for a new one.
func (w *refillWorld) addCard(m *modelTeam) {
	customer := m.customer
	if customer == "" || w.rng.IntN(3) == 0 {
		m.cards++
		customer = fmt.Sprintf("cus_%s_%d", m.name, m.cards)
	}
	if err := m.tenant.TestOnlySaveCard(w.t.Context(), store.Card{Customer: customer}, "billing", *w.now); err != nil {
		w.fatalf("save card: %v", err)
	}
	m.customer, m.cardAdded, w.hasCard[customer] = customer, *w.now, true
	w.logf("%s saved card %s", m.name, customer)
}

func (w *refillWorld) setSettings(m *modelTeam, settings store.RefillSettings) {
	if err := m.tenant.TestOnlySetRefill(w.t.Context(), settings); err != nil {
		w.fatalf("set refill %+v: %v", settings, err)
	}
	m.settings = settings
	w.logf("%s settings %+v", m.name, settings)
}

func (w *refillWorld) addFunds(m *modelTeam) {
	ctx := w.t.Context()
	cents := []int64{500, 2_000, 10_000}[w.rng.IntN(3)]
	checkout, err := m.tenant.TestOnlyOpenCreditCheckout(ctx, cents*store.MicroCreditsPerCent, *w.now, time.Hour)
	if err != nil {
		w.fatalf("open checkout: %v", err)
	}
	if err := m.tenant.TestOnlyAttachCreditCheckout(ctx, checkout, "cs_"+checkout, w.now.Add(time.Hour)); err != nil {
		w.fatalf("attach checkout: %v", err)
	}
	paid := capability.PaymentEvent{EventID: "evt_" + checkout, CheckoutID: "cs_" + checkout, PaymentID: "pi_" + checkout, Currency: "usd", AmountMinor: cents, Mode: capability.PaymentTest, Kind: capability.PaymentCheckoutPaid}
	if got, err := w.s.TestOnlySettlePayment(ctx, paid); err != nil || !got.Granted {
		w.fatalf("add funds = %+v, %v", got, err)
	}
	m.balance += cents * store.MicroCreditsPerCent
	m.addFunds = append(m.addFunds, *w.now)
	for _, r := range m.refills {
		r.cleared = r.cleared || r.failed
	}
	w.logf("%s added funds %d", m.name, cents)
	if st, err := m.tenant.SpendStanding(ctx); err != nil || st.Refill.Failure != "" {
		w.fatalf("%s: Add funds left refills blocked: %+v, %v", m.name, st.Refill, err)
	}
}

func (w *refillWorld) spend(m *modelTeam) {
	if m.balance <= 0 {
		return
	}
	micro := 1 + w.rng.Int64N(m.balance)
	if err := m.tenant.TestOnlySpendCredits(w.t.Context(), w.id("spend"), micro); err != nil {
		w.fatalf("spend: %v", err)
	}
	m.balance -= micro
	w.logf("%s spent %d micro", m.name, micro)
}

func (w *refillWorld) toggleHold(m *modelTeam) {
	ctx := w.t.Context()
	if m.hold {
		if _, err := m.tenant.ReleaseOperatorHolds(ctx, "operator", "reviewed"); err != nil {
			w.fatalf("release hold: %v", err)
		}
	} else if _, err := m.tenant.HoldByOperator(ctx, "operator", "review"); err != nil {
		w.fatalf("hold: %v", err)
	}
	m.hold = !m.hold
	w.logf("%s hold=%t", m.name, m.hold)
}

// ownerEvent is one event outside the refill pass and Stripe's webhooks.
func (w *refillWorld) ownerEvent(m *modelTeam) {
	switch n := w.rng.IntN(100); {
	case n < 30:
		w.setSettings(m, w.randomSettings())
	case n < 45:
		w.toggleHold(m)
	case n < 60:
		w.addFunds(m)
	case n < 80:
		w.spend(m)
	case n < 88:
		w.addCard(m)
	case n < 95:
		w.hasCard[m.customer] = false
		w.logf("%s removed its card in Stripe", m.name)
	default:
		w.hasCard[m.customer] = true
		w.logf("%s replaced its card in Stripe's portal", m.name)
	}
}

// checkout is the fake checkout service and Stripe behind it: an idempotent outcome per refill id, a 409 for a
// customer without a card, and failures before Stripe or after it with the answer lost.
func (w *refillWorld) checkout(m *modelTeam, r *modelRefill) (string, error) {
	if w.rng.IntN(10) == 0 {
		return "", errBeforeStripe
	}
	outcome, charged := w.outcomes[r.id]
	if !charged {
		if !w.hasCard[r.customer] {
			return "", errNoCard
		}
		switch n := w.rng.IntN(100); {
		case n < 70:
		case n < 85:
			outcome = "card_declined"
		default:
			outcome = "authentication_required"
		}
		w.outcomes[r.id] = outcome
		pi := "pi_" + r.id
		e := capability.PaymentEvent{
			EventID: "evt_" + pi, PaymentID: pi, ObjectID: pi, Currency: "usd", AmountMinor: r.amount, Mode: capability.PaymentTest,
			Kind: capability.PaymentCardPaid, CustomerID: r.customer, ChargeID: "ch_" + r.id, Status: "succeeded", Created: w.now.Unix(), RefillID: r.id,
		}
		if outcome == "" {
			r.captured, r.capturedAt = true, *w.now
		} else {
			e.Kind, e.Status, e.ChargeID, e.DeclineCode = capability.PaymentCardFailed, "requires_payment_method", "", outcome
		}
		w.pending = append(w.pending, modelWebhook{team: m, refill: r, event: e})
	}
	if w.rng.IntN(10) == 0 {
		return "", errAnswerLost
	}
	return outcome, nil
}

func (w *refillWorld) closeFailed(m *modelTeam, r *modelRefill) {
	if r.open() {
		r.closed, r.failed = *w.now, true
		if m.openID == r.id {
			m.openID = ""
		}
	}
}

func (w *refillWorld) recordFailure(m *modelTeam, r *modelRefill, code string) {
	got, err := w.s.AsOperator().RecordRefillFailed(w.t.Context(), store.Refill{Team: m.name, ID: r.id, AmountCents: r.amount, Customer: r.customer}, code)
	if err != nil {
		w.fatalf("record refill failure: %v", err)
	}
	if got.Replayed != !r.open() {
		w.fatalf("%s: recording %s on refill %s replayed=%t, model open=%t", m.name, code, r.id, got.Replayed, r.open())
	}
	w.closeFailed(m, r)
	w.logf("%s refill %s failed %s", m.name, r.id, code)
}

// charge is the controller's refill call for one authorized refill.
func (w *refillWorld) charge(visited map[store.Team]string, refill store.Refill) {
	m := w.byName[refill.Team]
	if m == nil {
		w.fatalf("a pass charged %s, a team outside this history", refill.Team)
	}
	// Another team's owner or operator acts between this team's call and the next team's authorization.
	if w.rng.IntN(3) == 0 && len(w.teams) > 1 {
		other := w.teams[w.rng.IntN(len(w.teams))]
		if other != m && visited[other.name] == "" {
			w.touched[other.name] = true
			w.logf("during the pass:")
			w.ownerEvent(other)
		}
	}
	visited[m.name] = refill.ID
	r := m.refills[refill.ID]
	if r == nil {
		if m.openID != "" {
			prev := m.refills[m.openID]
			if w.now.Sub(prev.opened) < refillModelKeyLife {
				w.fatalf("%s: refill %s opened while %s is open within its key life", m.name, refill.ID, prev.id)
			}
			prev.closed = *w.now
		}
		if refill.AmountCents != m.settings.AmountCents || refill.Customer != m.customer {
			w.fatalf("%s: new refill %+v, want amount %d customer %s", m.name, refill, m.settings.AmountCents, m.customer)
		}
		r = &modelRefill{id: refill.ID, customer: refill.Customer, amount: refill.AmountCents, opened: *w.now}
		m.refills[r.id], m.openID = r, r.id
		w.logf("%s opened refill %s for %d", m.name, r.id, r.amount)
	} else if !r.open() || refill.AmountCents != r.amount || refill.Customer != r.customer {
		w.fatalf("%s: retried %+v, model %+v", m.name, refill, *r)
	}
	var open int
	if err := w.s.DB().QueryRowContext(w.t.Context(), storetest.Rebind(w.s, `SELECT COUNT(*) FROM credit_checkouts WHERE team=? AND kind='refill' AND closed_at IS NULL`), string(m.name)).Scan(&open); err != nil || open != 1 {
		w.fatalf("%s: %d open refills, %v", m.name, open, err)
	}
	switch ok, why, gap := m.eligible(r, *w.now); {
	case !ok:
		w.fatalf("%s: charged refill %s while ineligible: %s", m.name, r.id, why)
	case gap:
		// A refill withdrawn or expired after a charge whose answer was lost counts against the limits only once its
		// signed payment arrives, so a refill opened before that webhook can pass a limit by one payment: the runbook
		// case, refunded in Stripe. A charge the controller heard about counts from its answer.
		*w.gaps++
		w.logf("%s: refill %s passes a limit by a charge the store has not seen: %s", m.name, r.id, why)
	}
	outcome, err := w.checkout(m, r)
	w.logf("%s called refill %s: outcome %q err %v", m.name, r.id, outcome, err)
	switch {
	case errors.Is(err, errNoCard):
		w.recordFailure(m, r, store.RefillNoCard)
	case err != nil:
	case outcome != "":
		w.recordFailure(m, r, outcome)
	default:
		if err := w.s.AsOperator().RecordRefillCharged(w.t.Context(), refill, "pi_"+r.id); err != nil {
			w.fatalf("record charged refill: %v", err)
		}
		r.answered = true
	}
}

func (w *refillWorld) pass() {
	type expectation struct {
		id   string
		call bool
	}
	// Teams without spending limits have no window the model counts differently from the store, so their pass is
	// predictable: an eligible open refill is retried as opened, and an eligible team without one gets a new one.
	expect := map[store.Team]expectation{}
	for _, m := range w.teams {
		if m.settings.WeekLimitCents != 0 || m.settings.MonthLimitCents != 0 {
			continue
		}
		if m.openID != "" {
			r := m.refills[m.openID]
			if w.now.Sub(r.opened) < refillModelKeyLife {
				ok, _, _ := m.eligible(r, *w.now)
				expect[m.name] = expectation{id: r.id, call: ok}
				continue
			}
		}
		ok, _, _ := m.eligible(nil, *w.now)
		expect[m.name] = expectation{call: ok}
	}
	w.touched = map[store.Team]bool{}
	visited := map[store.Team]string{}
	w.logf("refill pass")
	if err := w.s.AsOperator().EachDueRefill(w.t.Context(), func(r store.Refill) { w.charge(visited, r) }); err != nil {
		w.fatalf("refill pass: %v", err)
	}
	for _, m := range w.teams {
		if m.openID != "" && visited[m.name] != m.openID {
			m.refills[m.openID].closed = *w.now
			w.logf("%s refill %s closed by the pass (withdrawn or expired)", m.name, m.openID)
			m.openID = ""
		}
		want, ok := expect[m.name]
		if !ok || w.touched[m.name] {
			continue
		}
		got := visited[m.name]
		if want.call != (got != "") || (want.call && want.id != "" && got != want.id) {
			w.fatalf("%s: pass called %q, model expected call=%t id=%q", m.name, got, want.call, want.id)
		}
	}
}

func (w *refillWorld) deliver(i int, duplicate bool) {
	h := w.pending[i]
	if !duplicate {
		w.pending = slices.Delete(w.pending, i, i+1)
	}
	got, err := w.s.TestOnlySettlePayment(w.t.Context(), h.event)
	if err != nil {
		w.fatalf("webhook %s: %v", h.event.EventID, err)
	}
	r, m := h.refill, h.team
	if h.event.Kind == capability.PaymentCardPaid {
		if granted := got.Granted && !got.Replayed; granted == r.credited {
			w.fatalf("%s: paid webhook for %s granted=%t, already credited=%t (%+v)", m.name, r.id, got.Granted, r.credited, got)
		}
		if !r.credited {
			m.balance += r.amount * store.MicroCreditsPerCent
			r.credited = true
			if r.open() {
				r.closed = *w.now
				m.openID = ""
			}
		}
	} else {
		w.closeFailed(m, r)
	}
	w.logf("%s webhook %s for %s (duplicate kept %t)", m.name, h.event.Kind, r.id, duplicate)
}

func (w *refillWorld) advance() {
	step := []time.Duration{time.Minute, 10 * time.Minute, time.Hour, 6 * time.Hour, 25 * time.Hour, 8 * 24 * time.Hour}[w.rng.IntN(6)]
	next := w.now.Add(step)
	for i := len(w.pending) - 1; i >= 0; i-- {
		if r := w.pending[i].refill; r.opened.Add(refillModelWebhookBound).Before(next) {
			w.deliver(i, false)
		}
	}
	*w.now = next
	w.logf("clock +%s", step)
}

// check compares the store with the model after every step.
func (w *refillWorld) check() {
	ctx := w.t.Context()
	for _, m := range w.teams {
		st, err := m.tenant.SpendStanding(ctx)
		if err != nil {
			w.fatalf("%s standing: %v", m.name, err)
		}
		if st.BalanceMicro != m.balance {
			w.fatalf("%s: balance %d, model %d", m.name, st.BalanceMicro, m.balance)
		}
		if (st.Refill.Failure != "") != m.blocked() {
			w.fatalf("%s: store failure %q, model blocked %t", m.name, st.Refill.Failure, m.blocked())
		}
		if st.Refill.Open != (m.openID != "") {
			w.fatalf("%s: store open %t, model open %q", m.name, st.Refill.Open, m.openID)
		}
	}
}

// finish delivers every outcome, twice, and checks that each charge Stripe made granted credit exactly once.
func (w *refillWorld) finish() {
	for len(w.pending) > 0 {
		w.deliver(len(w.pending)-1, w.rng.IntN(2) == 0)
	}
	w.check()
	ctx := w.t.Context()
	for _, m := range w.teams {
		for _, r := range m.refills {
			var grants int
			if err := w.s.DB().QueryRowContext(ctx, storetest.Rebind(w.s, `SELECT COUNT(*) FROM credit_grants WHERE team=? AND reference=?`), string(m.name), "payment:test:"+r.id).Scan(&grants); err != nil {
				w.fatalf("grants: %v", err)
			}
			if want := map[bool]int{true: 1}[r.captured]; grants != want {
				w.fatalf("%s: refill %s captured=%t has %d grants", m.name, r.id, r.captured, grants)
			}
		}
		settings := m.settings
		settings.Enabled = false
		w.setSettings(m, settings)
	}
	w.pass()
	for _, m := range w.teams {
		if m.openID != "" {
			w.fatalf("%s: refill %s still open after refills were turned off", m.name, m.openID)
		}
	}
}

func runRefillHistory(t *testing.T, s *store.Store, now *time.Time, seed uint64, history int, gaps *int) {
	w := &refillWorld{
		t: t, s: s, gaps: gaps, rng: rand.New(rand.NewPCG(seed, uint64(history))), now: now, prefix: fmt.Sprintf("%d_%d", seed, history),
		byName: map[store.Team]*modelTeam{}, hasCard: map[string]bool{}, outcomes: map[string]string{},
	}
	for i := range 3 {
		w.addTeam(store.Team(fmt.Sprintf("rm%dh%d%c", seed, history, 'a'+i)))
	}
	w.check()
	for range refillModelSteps {
		switch n := w.rng.IntN(100); {
		case n < 35:
			w.pass()
		case n < 50:
			if len(w.pending) > 0 {
				w.deliver(w.rng.IntN(len(w.pending)), w.rng.IntN(4) == 0)
			}
		case n < 75:
			w.ownerEvent(w.teams[w.rng.IntN(len(w.teams))])
		default:
			w.advance()
		}
		w.check()
	}
	w.finish()
}

func refillModelSeeds(t *testing.T) ([]uint64, int) {
	seeds, histories := []uint64{1, 2, 3, 4, 5, 6, 7, 8}, 0
	switch v := os.Getenv(refillModelSeedEnv); v {
	case "":
	case "random":
		seed := uint64(time.Now().UnixNano())
		t.Logf("%s=%d", refillModelSeedEnv, seed)
		seeds = append(seeds, seed)
	default:
		seed, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatalf("%s=%q: %v", refillModelSeedEnv, v, err)
		}
		seeds = append(seeds, seed)
	}
	if v := os.Getenv(refillModelHistoriesEnv); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("%s=%q: want a positive count", refillModelHistoriesEnv, v)
		}
		histories = n
	}
	return seeds, histories
}

// Random histories of refill settings, holds, Add funds, spending, card changes, refill passes with a fake checkout
// service, webhooks early, late and duplicated, and clock jumps never charge an ineligible team, never open a second
// refill, credit every charge exactly once and keep refills stopped exactly while the model says they are.
func TestRefillModel(t *testing.T) {
	seeds, histories := refillModelSeeds(t)
	for _, seed := range seeds {
		t.Run(strconv.FormatUint(seed, 10), func(t *testing.T) {
			s := storetest.Open(t)
			now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
			s.SetTestClock(t, func() time.Time { return now })
			// A history takes about 0.1 s on SQLite and 1.3 s on PostgreSQL, so the normal suite runs 64 and 16.
			runs := histories
			if runs == 0 {
				runs = map[bool]int{false: 8, true: 2}[s.Dialect() == store.DialectPostgres]
			}
			var gaps int
			for history := range runs {
				runRefillHistory(t, s, &now, seed, history, &gaps)
			}
			if gaps > 0 {
				t.Logf("%d charges passed a limit by a lost-answer charge whose refill closed before its webhook", gaps)
			}
		})
	}
}

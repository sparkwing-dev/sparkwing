package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

const adminUsage = `usage: sparkwing-controller admin freeze --team SLUG --reason TEXT
       sparkwing-controller admin unfreeze --team SLUG --reason TEXT [--hold ID]
       sparkwing-controller admin grant --team SLUG --cents N --reason TEXT [--key KEY]

Break-glass for the operator console. When console sign-in is down, run it in
the controller pod with kubectl exec; it acts on the controller's state
database directly, beside the running controller, so the cluster access that
reaches the pod is the credential. It opens the store the running controller
declared (its --store and --credentials-dir, which the controller records
under SPARKWING_HOME at startup) and never creates one.

freeze holds the team: its metered claims are refused until the hold is
released, while work already running finishes. unfreeze releases the holds
the console and this command placed, or with --hold the one hold it names, a
payment dispute's included. grant gives the team N cents of free credits,
once per --key: a rerun with the same key reports the grant it already made,
and a run without --key grants under a fresh key. It refuses more than 10000
cents ($100) per grant unless SPARKWING_ADMIN_GRANT_ALLOW_LARGE=1 is set in the
pod. Each action commits the
business event the console writes (team.frozen, team.unfrozen or
credit.granted) with "sparkwing-controller admin" as the
actor, prints an audit line to stderr, and prints the team's holds, or the
grant and the team's balance, as JSON. Audit rows exclude free-form reasons
and store grant keys as digests.`

// safety: every business event this command writes names it as the actor, so a
// break-glass action never reads as a signed-in operator's.
const breakGlassActor = store.AuditActorBreakGlass

const (
	adminGrantCapCents   = 10_000
	adminGrantAllowLarge = "SPARKWING_ADMIN_GRANT_ALLOW_LARGE"
)

type adminFreezeJSON struct {
	Team   string   `json:"team"`
	Frozen bool     `json:"frozen"`
	Holds  []string `json:"holds"`
}

type adminGrantJSON struct {
	Team         string `json:"team"`
	GrantID      string `json:"grant_id"`
	AmountMicro  int64  `json:"amount_micro"`
	Reference    string `json:"reference"`
	Created      bool   `json:"created"`
	BalanceMicro int64  `json:"balance_micro"`
}

func runAdmin(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || (args[0] != "freeze" && args[0] != "unfreeze" && args[0] != "grant") {
		fmt.Fprintln(stderr, adminUsage)
		return errors.New("admin needs freeze, unfreeze or grant")
	}
	verb := args[0]
	fs := flag.NewFlagSet("admin "+verb, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(stderr, adminUsage) }
	team := fs.String("team", "", "the team's slug (required)")
	reason := fs.String("reason", "", "why (required, up to 500 characters; excluded from audit rows)")
	hold := fs.String("hold", "", "unfreeze only: the one hold to release, as the team's holds name it")
	cents := fs.Int64("cents", 0, "grant only: the free credits to grant, in cents (required)")
	key := fs.String("key", "", "grant only: makes a rerun report the first grant instead of granting again")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *team == "" {
		return errors.New("admin " + verb + " needs --team")
	}
	why, err := controller.OperatorReason(*reason)
	if err != nil {
		return fmt.Errorf("admin %s: %w", verb, err)
	}
	for _, name := range []string{"cents", "key"} {
		if fs.Changed(name) && verb != "grant" {
			return fmt.Errorf("--%s belongs to grant; %s takes none", name, verb)
		}
	}
	if verb == "grant" {
		if fs.Changed("hold") {
			return errors.New("--hold names a hold to release; grant takes none")
		}
		if *cents <= 0 || *cents > store.MaxCreditGrantMicro/store.MicroCreditsPerCent {
			return errors.New("admin grant needs --cents, a positive amount the credit ledger can hold")
		}
		if *cents > adminGrantCapCents && os.Getenv(adminGrantAllowLarge) != "1" {
			return fmt.Errorf("admin grant refuses more than %d cents ($%d) per grant; set %s=1 in the controller pod to allow a larger break-glass grant", adminGrantCapCents, adminGrantCapCents/100, adminGrantAllowLarge)
		}
		if fs.Changed("key") && strings.TrimSpace(*key) == "" {
			return errors.New("--key names no key; give one, or leave --key out to grant under a fresh key")
		}
	}
	holdID := strings.TrimSpace(*hold)
	if fs.Changed("hold") {
		if verb == "freeze" {
			return errors.New("--hold names a hold to release; freeze takes none")
		}
		// safety: a blank --hold must not fall through to releasing the
		// console's holds, let alone read as every hold on the team.
		if holdID == "" {
			return errors.New("--hold names no hold; give the hold's id, or leave --hold out")
		}
	}
	ctx := context.Background()
	p, err := paths.DefaultPaths()
	if err != nil {
		return err
	}
	st, err := openDeclaredStore(ctx, p.StateDB())
	if err != nil {
		return err
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			slog.New(controller.CredentialSafeLogHandler(slog.NewJSONHandler(stderr, nil))).Error("close state", "err", cerr)
		}
	}()
	t, err := st.ForTeam(ctx, store.Team(*team))
	if err != nil {
		return err
	}
	if verb == "grant" {
		return adminGrant(ctx, t, *cents, strings.TrimSpace(*key), why, stdout, stderr)
	}
	var changed []string
	switch {
	case verb == "freeze":
		id, err := t.HoldByOperator(ctx, breakGlassActor, why)
		if err != nil {
			return err
		}
		changed = []string{id}
	case holdID != "":
		n, err := t.ReleaseCreditFreezes(ctx, holdID, breakGlassActor, why)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("team %s holds no hold %q", t.Team(), holdID)
		}
		changed = []string{holdID}
	default:
		if _, err := t.ReleaseOperatorHolds(ctx, breakGlassActor, why); err != nil {
			return err
		}
	}
	freeze, err := t.CreditFreeze(ctx)
	if err != nil {
		return err
	}
	slog.New(controller.CredentialSafeLogHandler(slog.NewJSONHandler(stderr, nil))).Info("audit",
		"command", breakGlassActor+" "+verb, "principal_kind", "break-glass", "principal_id", breakGlassActor,
		"target_team", string(t.Team()), "holds_changed", changed, "frozen", freeze.Frozen)
	holds := append([]string{}, freeze.Disputes...)
	return json.NewEncoder(stdout).Encode(adminFreezeJSON{Team: string(t.Team()), Frozen: freeze.Frozen, Holds: holds})
}

func adminGrant(ctx context.Context, t *store.Tenant, cents int64, key, reason string, stdout, stderr io.Writer) error {
	if key == "" {
		key = strings.ToLower(rand.Text())
	}
	result, err := t.RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: cents * store.MicroCreditsPerCent, Reference: "admin:" + key, CreatedBy: breakGlassActor, Reason: reason})
	if err != nil {
		return err
	}
	balance, err := t.CreditBalanceMicro(ctx)
	if err != nil {
		return err
	}
	slog.New(controller.CredentialSafeLogHandler(slog.NewJSONHandler(stderr, nil))).Info("audit",
		"command", breakGlassActor+" grant", "principal_kind", "break-glass", "principal_id", breakGlassActor,
		"target_team", string(t.Team()), "grant_id", result.Grant.ID, "amount_micro", result.Grant.AmountMicro, "created", result.Created)
	return json.NewEncoder(stdout).Encode(adminGrantJSON{Team: string(t.Team()), GrantID: result.Grant.ID, AmountMicro: result.Grant.AmountMicro, Reference: result.Grant.Reference, Created: result.Created, BalanceMicro: balance})
}

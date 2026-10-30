package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

const adminUsage = `usage: sparkwing-controller admin freeze --operator ACCOUNT_ID --team SLUG --reason TEXT
       sparkwing-controller admin unfreeze --operator ACCOUNT_ID --team SLUG --reason TEXT [--hold ID]
       sparkwing-controller admin session --operator ACCOUNT_ID --team SLUG --reason TEXT [--output json|plain]
       sparkwing-controller admin grant --operator ACCOUNT_ID --team SLUG --cents N --reason TEXT --key KEY [--reverses REF]

Break-glass for the operator console. When console sign-in is down, run it in
the controller pod with kubectl exec; it acts on the controller's state
database directly, beside the running controller, so the cluster access that
reaches the pod is the credential. It opens the running controller's recorded
store configuration under SPARKWING_HOME and never creates a store.

session prints an independent account session for an existing team administrator
on a licensed team or Cloud controller.
Plain output is the default for piping to sparkwing runners add --session-stdin.
Use --output json for a JSON object containing the session. Its initial expiry is 15 minutes.
Normal renewal applies, including during enrollment. After fetching the session
CSRF proof, enrollment attempts logout on success or failure. If fetching the
proof or logout fails, the session remains live until expiry or revocation. The session is printed only
to stdout. The session.break_glass audit row records the account and team,
excluding the session and free-form reason.

freeze holds the team: its metered claims are refused until the hold is
released. Running Cloud work stops at its funded deadline. unfreeze releases the holds
the console and this command placed, or with --hold the one hold it names, a
payment dispute's included. grant gives the team N cents of free credits,
once per required --key. A rerun reports the grant it already made.
Use --reverses REF to take back credits from a mistaken free grant.
--operator names your account ID. Each action commits the business event the
console writes (team.frozen, team.unfrozen or credit.granted or credit.reversed) with that account
as the actor, prints an audit line to stderr, and prints the team's holds, or the
grant and the team's balance, as JSON. Audit rows exclude free-form reasons
and store grant keys as digests.`

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
	if len(args) == 0 || (args[0] != "freeze" && args[0] != "unfreeze" && args[0] != "grant" && args[0] != "session") {
		fmt.Fprintln(stderr, adminUsage)
		return errors.New("admin needs freeze, unfreeze, grant or session")
	}
	verb := args[0]
	fs := flag.NewFlagSet("admin "+verb, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(stderr, adminUsage) }
	output := fs.StringP("output", "o", "plain", "session only: plain (default) or json")
	operator := fs.String("operator", "", "your operator account ID (required)")
	team := fs.String("team", "", "the team's slug (required)")
	reason := fs.String("reason", "", "why (required, up to 500 characters; excluded from audit rows)")
	hold := fs.String("hold", "", "unfreeze only: the one hold to release, as the team's holds name it")
	cents := fs.Int64("cents", 0, "grant only: the free credits to grant, in cents (required)")
	key := fs.String("key", "", "grant only: required idempotency key")
	reverses := fs.String("reverses", "", "grant only: reference of a free grant to correct")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.Changed("output") && verb != "session" {
		return errors.New("--output belongs to session")
	}
	if *output != "json" && *output != "plain" {
		return errors.New("--output must be json or plain")
	}
	if *operator == "" || strings.ContainsAny(*operator, "@\r\n\t ") {
		return errors.New("admin " + verb + " needs --operator with your account ID")
	}
	if *team == "" {
		return errors.New("admin " + verb + " needs --team")
	}
	why, err := controller.OperatorReason(*reason)
	if err != nil {
		return fmt.Errorf("admin %s: %w", verb, err)
	}
	for _, name := range []string{"cents", "key", "reverses"} {
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
		if fs.Changed("reverses") && strings.TrimSpace(*reverses) == "" {
			return errors.New("--reverses requires the free grant's reference")
		}
		if strings.TrimSpace(*key) == "" {
			return errors.New("admin grant requires --key for idempotency")
		}
	}
	holdID := strings.TrimSpace(*hold)
	if fs.Changed("hold") {
		if verb != "unfreeze" {
			return errors.New("--hold belongs to unfreeze")
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
	var opts []store.Option
	if verb == "session" {
		opts = append(opts, store.WithPolicies(func(st *store.Store) (store.Policies, error) {
			body, err := os.ReadFile(deploymentRecordPath(p.StateDB()))
			if err != nil {
				return store.Policies{}, err
			}
			var rec deploymentRecord
			if err := json.Unmarshal(body, &rec); err != nil {
				return store.Policies{}, err
			}

			if rec.Kind != deployment.KindTeam && rec.Kind != deployment.KindCloud {
				return store.Policies{}, errors.New("admin session requires a licensed team or Cloud controller; local execution uses the local controller connection")
			}
			creds, err := readCredentials(rec.CredentialsDir)
			if err != nil {
				return store.Policies{}, err
			}
			keys, err := deployment.NewKeyPair(credSecretsKey, credSecretsPreviousKey, creds.SecretsKey, creds.SecretsPreviousKey)
			if err != nil {
				return store.Policies{}, err
			}
			cipher, err := secretsCipher(keys)
			if cipher == nil && err == nil {
				return store.Policies{}, errors.New("admin session needs the serving controller's secrets-key credential")
			}
			if err != nil {
				return store.Policies{}, err
			}
			return store.Policies{SessionKeys: cipher}, nil
		}))
	}
	st, err := openDeclaredStore(ctx, p.StateDB(), opts...)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			slog.New(controller.CredentialSafeLogHandler(slog.NewJSONHandler(stderr, nil))).Error("close state", "err", cerr)
		}
	}()
	if _, err := st.AsOperator().Account(ctx, *operator); err != nil {
		return fmt.Errorf("admin operator account: %w", err)
	}
	t, err := st.ForTeam(ctx, store.Team(*team))
	if err != nil {
		return err
	}
	if verb == "session" {
		role, err := t.MemberRole(ctx, *operator)
		if err != nil || role != store.RoleOwner {
			return errors.New("admin session requires an existing team administrator in --team")
		}
		raw, _, _, err := st.AsOperator().CreateBreakGlassSession(ctx, *operator, t.Team(), 15*time.Minute)
		if err != nil {
			return err
		}
		slog.New(controller.CredentialSafeLogHandler(slog.NewJSONHandler(stderr, nil))).Info("audit",
			"command", "sparkwing-controller admin session", "principal_kind", "break-glass", "principal_id", *operator,
			"target_team", string(t.Team()))
		if *output == "json" {
			return json.NewEncoder(stdout).Encode(struct {
				Session string `json:"session"`
			}{Session: raw})
		}
		_, err = fmt.Fprintln(stdout, raw)
		return err
	}
	if verb == "grant" {
		return adminGrant(ctx, t, *operator, *cents, strings.TrimSpace(*key), strings.TrimSpace(*reverses), why, stdout, stderr)
	}
	var changed []string
	switch {
	case verb == "freeze":
		id, err := t.HoldByOperator(ctx, *operator, why)
		if err != nil {
			return err
		}
		changed = []string{id}
	case holdID != "":
		n, err := t.ReleaseCreditFreezes(ctx, holdID, *operator, why)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("team %s holds no hold %q", t.Team(), holdID)
		}
		changed = []string{holdID}
	default:
		if _, err := t.ReleaseOperatorHolds(ctx, *operator, why); err != nil {
			return err
		}
	}
	freeze, err := t.CreditFreeze(ctx)
	if err != nil {
		return err
	}
	slog.New(controller.CredentialSafeLogHandler(slog.NewJSONHandler(stderr, nil))).Info("audit",
		"command", "sparkwing-controller admin "+verb, "principal_kind", "break-glass", "principal_id", *operator,
		"target_team", string(t.Team()), "holds_changed", changed, "frozen", freeze.Frozen)
	holds := append([]string{}, freeze.Disputes...)
	return json.NewEncoder(stdout).Encode(adminFreezeJSON{Team: string(t.Team()), Frozen: freeze.Frozen, Holds: holds})
}

func adminGrant(ctx context.Context, t *store.Tenant, operator string, cents int64, key, reverses, reason string, stdout, stderr io.Writer) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("admin grant requires --key for idempotency")
	}
	kind, amount := store.CreditGrantFree, cents*store.MicroCreditsPerCent
	if reverses != "" {
		kind, amount = "reversal", -amount
	}
	result, err := t.RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: kind, AmountMicro: amount, Reverses: reverses, Reference: "admin:" + key, CreatedBy: operator, Reason: reason})
	if err != nil {
		return err
	}
	balance, err := t.CreditBalanceMicro(ctx)
	if err != nil {
		return err
	}
	slog.New(controller.CredentialSafeLogHandler(slog.NewJSONHandler(stderr, nil))).Info("audit",
		"command", "sparkwing-controller admin grant", "principal_kind", "break-glass", "principal_id", operator,
		"target_team", string(t.Team()), "grant_id", result.Grant.ID, "amount_micro", result.Grant.AmountMicro, "created", result.Created)
	return json.NewEncoder(stdout).Encode(adminGrantJSON{Team: string(t.Team()), GrantID: result.Grant.ID, AmountMicro: result.Grant.AmountMicro, Reference: result.Grant.Reference, Created: result.Created, BalanceMicro: balance})
}

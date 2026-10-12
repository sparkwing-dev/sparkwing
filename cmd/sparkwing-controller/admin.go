package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/credentials"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const adminUsage = `usage: sparkwing-controller admin freeze --team SLUG --reason TEXT [--credentials-dir DIR]
       sparkwing-controller admin unfreeze --team SLUG --reason TEXT [--hold ID] [--credentials-dir DIR]

Break-glass for the operator console. When console sign-in is down, run it in
the controller pod with kubectl exec; it acts on the controller's state
database directly, beside the running controller, so the cluster access that
reaches the pod is the credential.

freeze holds the team: its metered claims are refused until the hold is
released, while work already running finishes. unfreeze releases the holds
the console and this command placed, or with --hold the one hold it names, a
payment dispute's included. Each action commits the business event the
console writes (team.frozen or team.unfrozen) with the reason and with
"sparkwing-controller admin" as the actor, prints an audit line to stderr,
and prints the team's holds as JSON.`

// safety: every business event this command writes names it as the actor, so a
// break-glass action never reads as a signed-in operator's.
const breakGlassActor = "sparkwing-controller admin"

type adminFreezeJSON struct {
	Team   string   `json:"team"`
	Frozen bool     `json:"frozen"`
	Holds  []string `json:"holds"`
}

func runAdmin(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || (args[0] != "freeze" && args[0] != "unfreeze") {
		fmt.Fprintln(stderr, adminUsage)
		return errors.New("admin needs freeze or unfreeze")
	}
	verb := args[0]
	fs := flag.NewFlagSet("admin "+verb, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(stderr, adminUsage) }
	team := fs.String("team", "", "the team's slug (required)")
	reason := fs.String("reason", "", "why, recorded on the business event (required, up to 500 characters)")
	hold := fs.String("hold", "", "unfreeze only: the one hold to release, as the team's holds name it")
	credentialsDir := fs.String(credentials.FlagName, "",
		"the controller's credentials directory; its "+credPGURL+" file selects the PostgreSQL store")
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
	creds, err := readCredentials(*credentialsDir)
	if err != nil {
		return err
	}
	st, err := openControllerStore(ctx, p.StateDB(), creds.PGURL)
	if err != nil {
		return mapStoreOpenError(err)
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			fmt.Fprintln(stderr, "sparkwing-controller: close state:", cerr)
		}
	}()
	t, err := st.ForTeam(ctx, store.Team(*team))
	if err != nil {
		return err
	}
	now := time.Now()
	var changed []string
	switch {
	case verb == "freeze":
		id, err := t.HoldByOperator(ctx, breakGlassActor, why, now)
		if err != nil {
			return err
		}
		changed = []string{id}
	case holdID != "":
		n, err := st.ReleaseCreditFreezes(ctx, t.Team(), holdID, breakGlassActor, why, now)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("team %s holds no hold %q", t.Team(), holdID)
		}
		changed = []string{holdID}
	default:
		if _, err := t.ReleaseOperatorHolds(ctx, breakGlassActor, why, now); err != nil {
			return err
		}
	}
	freeze, err := t.CreditFreeze(ctx)
	if err != nil {
		return err
	}
	slog.New(slog.NewJSONHandler(stderr, nil)).Info("audit",
		"command", breakGlassActor+" "+verb, "principal_kind", "break-glass", "principal_id", breakGlassActor,
		"target_team", string(t.Team()), "holds_changed", changed, "frozen", freeze.Frozen)
	holds := append([]string{}, freeze.Disputes...)
	return json.NewEncoder(stdout).Encode(adminFreezeJSON{Team: string(t.Team()), Frozen: freeze.Frozen, Holds: holds})
}

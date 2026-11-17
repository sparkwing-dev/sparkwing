package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

func runAccountDelete(ctx context.Context, inv clisurface.Invocation) error {
	profiles, _, err := loadProfiles()
	if err != nil {
		return err
	}
	profile, ok := profiles[inv.Value("profile")]
	if !ok {
		return fmt.Errorf("profile %q does not exist", inv.Value("profile"))
	}
	session, err := readTokenStdin(os.Stdin, os.Stderr, "existing account session")
	if err != nil {
		return err
	}
	d, err := client.NewAccountDeleter(ctx, client.EngineOptions{URL: profile.Controller.URL}, session)
	if err != nil {
		return err
	}
	return runAccountDeleteCommand(ctx, inv, os.Stdout, d)
}

type accountDeleter interface {
	Preview(context.Context) (client.AccountDeletionPreview, error)
	Delete(context.Context, string, []string) (client.AccountDeletionResult, error)
}

func runAccountDeleteCommand(ctx context.Context, inv clisurface.Invocation, out io.Writer, d accountDeleter) error {
	plan, err := d.Preview(ctx)
	if err != nil {
		return err
	}
	format := inv.Output(color.IsInteractiveStdout())
	if inv.Value("confirm") == "" {
		switch format {
		case "json":
			err = json.NewEncoder(out).Encode(plan)
		case "plain":
			for _, value := range append([]string{plan.AccountID}, plan.Teams...) {
				if _, err = fmt.Fprintln(out, value); err != nil {
					return err
				}
			}
			for _, team := range plan.BlockedTeams {
				if _, err = fmt.Fprintln(out, "blocked_team="+team); err != nil {
					return err
				}
			}
			if _, err = fmt.Fprintln(out, "sign_in_required="+fmt.Sprint(plan.SignInRequired)); err != nil {
				return err
			}
			if _, err = fmt.Fprintln(out, plan.CredentialNotice); err != nil {
				return err
			}
			_, err = fmt.Fprintln(out, plan.ArchiveNotice)
		default:
			_, err = fmt.Fprintf(out, "Account: %s\nTeams deleted with this account: %s\nBlocked teams: %s\nTheir runs and data will be deleted. Shared-team history stays. Selected execution actor fields are relabeled; some creator references retain your opaque account ID.\nAll account tokens and sessions are revoked immediately. %s\nOn Sparkwing Cloud, RDS automated backups use a seven-day recovery window, but not every copy expires within seven days. %s Self-hosted backup retention follows the operator policy.\nFinancial records and audit history remain (audit default: 400 days). OIDC audit audience and subject strings can include personal text and are retained.\nSee sparkwing docs read --topic account-deletion-retention.\nConfirm with --confirm %s and --confirm-team SLUG for each listed team.\n", plan.AccountID, strings.Join(plan.Teams, ", "), strings.Join(plan.BlockedTeams, ", "), plan.CredentialNotice, plan.ArchiveNotice, plan.AccountID)
		}
		if err != nil {
			return err
		}
		return accountDeletionReadiness(plan)
	}
	if err := accountDeletionReadiness(plan); err != nil {
		return err
	}
	if len(plan.Teams) > 0 && len(inv.Flags["confirm-team"]) == 0 {
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "--confirm-team is missing; repeat --confirm-team for every listed team: " + strings.Join(plan.Teams, ", ")}
	}
	result, err := d.Delete(ctx, inv.Value("confirm"), inv.Flags["confirm-team"])
	if err != nil {
		return err
	}
	if format == "json" {
		return json.NewEncoder(out).Encode(result)
	}
	if format == "plain" {
		_, err = fmt.Fprintln(out, result.AccountID)
		return err
	}
	_, err = fmt.Fprintf(out, "Deleted account %s. Teams queued for deletion: %s. All account sessions and tokens are revoked.\n", result.AccountID, strings.Join(result.DeletedTeams, ", "))
	return err
}

func accountDeletionReadiness(plan client.AccountDeletionPreview) error {
	if len(plan.BlockedTeams) > 0 {
		return fmt.Errorf("transfer ownership or delete these teams first: %s", strings.Join(plan.BlockedTeams, ", "))
	}
	if plan.SignInRequired {
		return fmt.Errorf("sign out of the dashboard and sign in again, then supply the new direct account session with --session-stdin within 10 minutes")
	}
	return nil
}

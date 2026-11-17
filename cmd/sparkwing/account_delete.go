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
		if format == "json" {
			return json.NewEncoder(out).Encode(plan)
		}
		if format == "plain" {
			for _, value := range append([]string{plan.AccountID}, plan.Teams...) {
				if _, err := fmt.Fprintln(out, value); err != nil {
					return err
				}
			}
			return nil
		}
		_, err = fmt.Fprintf(out, "Account: %s\nTeams deleted with this account: %s\nTheir runs and data will be deleted. Shared-team history stays.\nAll account tokens and sessions are revoked immediately. Operational team data is purged within minutes; Cloud backup copies expire within 7 days; self-hosted backups follow the operator policy. Financial records and audit history remain (audit default: 400 days). OIDC audit audience and subject strings can include personal text and are retained.\nConfirm with --confirm %s and --confirm-team SLUG for each listed team.\n", plan.AccountID, strings.Join(plan.Teams, ", "), plan.AccountID)
		if err != nil {
			return err
		}
		if len(plan.BlockedTeams) > 0 {
			return fmt.Errorf("transfer ownership or delete these teams first: %s", strings.Join(plan.BlockedTeams, ", "))
		}
		return nil
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

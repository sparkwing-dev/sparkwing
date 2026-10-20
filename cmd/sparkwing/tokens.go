package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

func runTokenCommand(ctx context.Context, inv clisurface.Invocation) error {
	deployment := strings.HasPrefix(inv.Command.Path, "sparkwing operator ")
	if inv.Help() || inv.Command.Path == "sparkwing tokens" || inv.Command.Path == "sparkwing operator tokens" {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, inv.Output(color.IsInteractiveStdout()))
	}
	if _, _, err := parseTokenMutation(inv); err != nil {
		return err
	}
	options, closeTransport, err := controllerOptions(ctx, inv.Value("profile"))
	if err != nil {
		return err
	}
	defer closeTransport()
	manager, err := client.NewTokenManager(options, deployment)
	if err != nil {
		return err
	}
	return manageTokens(ctx, inv, manager, os.Stdout)
}

func parseTokenMutation(inv clisurface.Invocation) (controllerwire.TokenCreate, controllerwire.TokenRotate, error) {
	create := controllerwire.TokenCreate{AllowTeamAdmin: inv.Bool("allow-team-admin"), Kind: inv.Value("type"), Principal: inv.Value("principal")}
	for _, scope := range inv.Flags["scope"] {
		create.Scopes = append(create.Scopes, capability.Grant(scope))
	}
	rotate := controllerwire.TokenRotate{GraceSecs: int64((24 * time.Hour) / time.Second)}
	seconds := func(flag string) (*int64, error) {
		if !inv.Has(flag) {
			return nil, nil
		}
		d, err := clisurface.ParseLookback(inv.Value(flag))
		if err != nil || d < 0 || d%time.Second != 0 {
			return nil, &clisurface.UsageError{Path: inv.Command.Path, Message: "--" + flag + " must be a nonnegative duration in whole seconds"}
		}
		n := int64(d / time.Second)
		return &n, nil
	}
	ttl, err := seconds("ttl")
	if err != nil {
		return create, rotate, err
	}
	create.TTLSecs, rotate.TTLSecs = ttl, ttl
	grace, err := seconds("grace")
	if grace != nil {
		rotate.GraceSecs = *grace
	}
	return create, rotate, err
}

func manageTokens(ctx context.Context, inv clisurface.Invocation, manager controllerwire.TokenManager, out io.Writer) error {
	create, rotate, err := parseTokenMutation(inv)
	if err != nil {
		return err
	}
	format := inv.Output(color.IsInteractiveStdout())
	verb := inv.Command.Path[strings.LastIndexByte(inv.Command.Path, ' ')+1:]
	switch verb {
	case "list":
		if inv.Has("token") {
			token, err := manager.ReadToken(ctx, inv.Value("token"))
			if err != nil {
				return err
			}
			return writeTokenMetadata(out, []controllerwire.TokenMetadata{token}, format)
		}
		tokens, err := manager.ListTokens(ctx, controllerwire.TokenQuery{Kind: inv.Value("type"), IncludeRevoked: inv.Bool("include-revoked")})
		if err != nil {
			return err
		}
		return writeTokenMetadata(out, tokens.Tokens, format)
	case "create":
		create.IdempotencyKey = rand.Text()
		created, err := manager.CreateToken(ctx, create)
		if err != nil {
			return err
		}
		if format == "json" {
			return json.NewEncoder(out).Encode(created)
		}
		if _, err := fmt.Fprintln(out, created.Token); err != nil {
			return err
		}
		if format == "pretty" {
			return writeTokenMetadata(out, []controllerwire.TokenMetadata{created.Metadata}, format)
		}
		return nil
	case "rotate":
		rotate.IdempotencyKey = rand.Text()
		rotated, err := manager.RotateToken(ctx, inv.Value("token"), rotate)
		if err != nil {
			return err
		}
		if format == "json" {
			return json.NewEncoder(out).Encode(rotated)
		}
		if _, err := fmt.Fprintln(out, rotated.Token); err != nil {
			return err
		}
		if format == "pretty" {
			if _, err := fmt.Fprintf(out, "old token %s revokes at %s\n", rotated.OldPrefix, time.Unix(rotated.OldRevokedAt, 0).UTC().Format(time.RFC3339)); err != nil {
				return err
			}
			return writeTokenMetadata(out, []controllerwire.TokenMetadata{rotated.Metadata}, format)
		}
		return nil
	case "revoke":
		result, err := manager.RevokeToken(ctx, inv.Value("token"))
		if err != nil {
			return err
		}
		if format == "json" {
			return json.NewEncoder(out).Encode(result)
		}
		_, err = fmt.Fprintln(out, result.Prefix)
		return err
	}
	return &clisurface.UsageError{Path: inv.Command.Path, Message: "unknown token command"}
}

func writeTokenMetadata(out io.Writer, tokens []controllerwire.TokenMetadata, format string) error {
	if format == "json" {
		for _, token := range tokens {
			if err := json.NewEncoder(out).Encode(token); err != nil {
				return err
			}
		}
		return nil
	}
	if format == "plain" {
		for _, token := range tokens {
			if _, err := fmt.Fprintln(out, token.Prefix); err != nil {
				return err
			}
		}
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PREFIX\tTYPE\tPRINCIPAL\tSCOPES\tMETERED\tLAST USED\tREVOKED")
	for _, token := range tokens {
		scopes := make([]string, len(token.Scopes))
		for i, scope := range token.Scopes {
			scopes[i] = string(scope)
		}
		last, revoked := "-", "-"
		if token.LastUsedAt != nil {
			last = time.Unix(*token.LastUsedAt, 0).UTC().Format(time.RFC3339)
		}
		if token.RevokedAt != nil {
			revoked = time.Unix(*token.RevokedAt, 0).UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%t\t%s\t%s\n", token.Prefix, token.Kind, token.Principal, joinOrDash(scopes), token.Metered, last, revoked)
	}
	return tw.Flush()
}

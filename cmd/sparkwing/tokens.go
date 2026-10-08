package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/ndjson"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"

	flag "github.com/spf13/pflag"
)

func runTokens(args []string) error {
	if handleParentHelp(cmdTokens, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdTokens, os.Stderr)
		return fmt.Errorf("tokens: subcommand required (create|list|revoke|rotate)")
	}
	switch args[0] {
	case "create":
		return runTokensCreate(args[1:])
	case "list":
		return runTokensList(args[1:])
	case "revoke":
		return runTokensRevoke(args[1:])
	case "rotate":
		return runTokensRotate(args[1:])
	default:
		PrintHelp(cmdTokens, os.Stderr)
		return fmt.Errorf("tokens: unknown subcommand %q", args[0])
	}
}

func runTokensCreate(args []string) error {
	fs := flag.NewFlagSet(cmdTokensCreate.Path, flag.ContinueOnError)
	on := addProfileFlag(fs)
	kind := fs.String("type", "", "token type: user|runner|service")
	principal := fs.String("principal", "", "free-form label identifying the token holder")
	scopes := fs.String("scope", "", "comma-separated scopes")
	ttl := fs.Duration("ttl", 0, "token lifetime (0 = never expires)")
	if err := parseAndCheck(cmdTokensCreate, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}

	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "tokens create"); err != nil {
		return err
	}

	req := client.CreateTokenRequest{Kind: *kind, Principal: *principal, Scopes: splitCSV(*scopes)}
	if *ttl > 0 {
		req.TTLSecs = int64((*ttl).Seconds())
	}
	out, err := client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken()).CreateToken(context.Background(), req)
	if err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "WARNING: stash this token NOW. It is not recoverable after this command exits.")
	fmt.Println(out.Token)
	fmt.Fprintln(os.Stderr, "---")
	fmt.Fprintln(os.Stderr, string(out.Metadata))
	return nil
}

func runTokensList(args []string) error {
	fs := flag.NewFlagSet(cmdTokensList.Path, flag.ContinueOnError)
	on := addProfileFlag(fs)
	kind := fs.String("type", "", "filter by type (user|runner|service)")
	includeRevoked := fs.Bool("include-revoked", false, "include revoked tokens")
	prefix := fs.String("prefix", "", "print the full record of the one token with this non-secret prefix")
	outputFormat := fs.StringP("output", "o", "", "output format (json|table)")
	if err := parseAndCheck(cmdTokensList, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}

	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "tokens list"); err != nil {
		return err
	}
	c := client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken())
	if *prefix != "" {
		return printTokenRecord(c, *prefix)
	}
	tokens, err := c.ListTokens(context.Background(), *kind, *includeRevoked)
	if err != nil {
		return err
	}

	if *outputFormat == "json" {
		return renderTokensJSON(os.Stdout, tokens)
	}
	return renderTokensTable(os.Stdout, tokens)
}

func renderTokensJSON(w io.Writer, tokens []client.TokenInfo) error {
	return ndjson.Write(w, tokens)
}

func renderTokensTable(w io.Writer, tokens []client.TokenInfo) error {
	if len(tokens) == 0 {
		_, err := fmt.Fprintln(w, "(no tokens)")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PREFIX\tTYPE\tPRINCIPAL\tSCOPES\tMETERED\tLAST_USED")
	for _, t := range tokens {
		lastUsed := "-"
		if t.LastUsedAt != nil {
			lastUsed = time.Unix(*t.LastUsedAt, 0).UTC().Format("2006-01-02 15:04")
		}
		if t.RevokedAt != nil {
			lastUsed += " (revoked)"
		}
		metered := "-"
		if t.Metered {
			metered = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			t.Prefix, t.Kind, t.Principal, formatScopes(t.Scopes), metered, lastUsed)
	}
	return tw.Flush()
}

func formatScopes(scopes []string) string {
	if len(scopes) == 0 {
		return "-"
	}
	if slices.Contains(scopes, "admin") {
		return "*"
	}
	return strings.Join(scopes, ",")
}

func runTokensRevoke(args []string) error {
	fs := flag.NewFlagSet(cmdTokensRevoke.Path, flag.ContinueOnError)
	on := addProfileFlag(fs)
	prefix := fs.String("prefix", "", "non-secret token prefix (from 'tokens list')")
	if err := parseAndCheck(cmdTokensRevoke, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "tokens revoke"); err != nil {
		return err
	}
	if err := client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken()).
		RevokeToken(context.Background(), *prefix); err != nil {
		return err
	}
	fmt.Printf("revoked %s\n", *prefix)
	return nil
}

func printTokenRecord(c *client.Client, prefix string) error {
	resp, err := c.LookupToken(context.Background(), prefix)
	if err != nil {
		return err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, resp, "", "  "); err != nil {
		fmt.Println(string(resp))
	} else {
		fmt.Println(pretty.String())
	}
	return nil
}

func runTokensRotate(args []string) error {
	fs := flag.NewFlagSet(cmdTokensRotate.Path, flag.ContinueOnError)
	on := addProfileFlag(fs)
	prefix := fs.String("prefix", "", "non-secret token prefix")
	grace := fs.Duration("grace", 24*time.Hour, "window during which the old token still authenticates (max 168h)")
	ttl := fs.Duration("ttl", 0, "TTL of the new token (0 = preserve the old token's remaining TTL)")
	if err := parseAndCheck(cmdTokensRotate, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "tokens rotate"); err != nil {
		return err
	}
	out, err := client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken()).
		RotateToken(context.Background(), *prefix, *grace, *ttl)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "WARNING: stash this new token NOW. The old token continues working until the grace window closes.")
	fmt.Println(out.Token)
	fmt.Fprintf(os.Stderr, "---\nold prefix=%s revoked_at=%d\nnew metadata: %s\n",
		*prefix, out.OldRevokedAt, string(out.New))
	return nil
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

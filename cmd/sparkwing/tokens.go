// `sparkwing tokens` subcommand. Manages the controller's tokens
// table over HTTP.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	flag "github.com/spf13/pflag"
)

func runTokens(args []string) error {
	if handleParentHelp(cmdTokens, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdTokens, os.Stderr)
		return fmt.Errorf("tokens: subcommand required (create|list|revoke|lookup|rotate)")
	}
	switch args[0] {
	case "create":
		return runTokensCreate(args[1:])
	case "list":
		return runTokensList(args[1:])
	case "revoke":
		return runTokensRevoke(args[1:])
	case "lookup":
		return runTokensLookup(args[1:])
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

	body := map[string]any{
		"kind":      *kind, // wire shape unchanged -- controller still reads `kind`
		"principal": *principal,
		"scopes":    splitCSV(*scopes),
	}
	if *ttl > 0 {
		body["ttl_secs"] = int64((*ttl).Seconds())
	}
	resp, err := tokensPost(prof.Controller, prof.Token, "/api/v1/tokens", body)
	if err != nil {
		return err
	}
	var out struct {
		Token    string          `json:"token"`
		Metadata json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return fmt.Errorf("decode: %w", err)
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
	q := url("")
	if *kind != "" {
		q = q.with("kind", *kind)
	}
	if *includeRevoked {
		q = q.with("include_revoked", "1")
	}
	resp, err := tokensGet(prof.Controller, prof.Token, "/api/v1/tokens"+q.encode())
	if err != nil {
		return err
	}
	var out struct {
		Tokens []map[string]any `json:"tokens"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if len(out.Tokens) == 0 {
		fmt.Println("(no tokens)")
		return nil
	}
	fmt.Printf("%-14s %-8s %-20s %-40s %s\n", "PREFIX", "TYPE", "PRINCIPAL", "SCOPES", "LAST_USED")
	for _, t := range out.Tokens {
		scopes := ""
		if ss, ok := t["scopes"].([]any); ok {
			parts := make([]string, 0, len(ss))
			for _, s := range ss {
				parts = append(parts, fmt.Sprint(s))
			}
			scopes = strings.Join(parts, ",")
		}
		lastUsed := "-"
		if v, ok := t["last_used_at"].(float64); ok {
			lastUsed = time.Unix(int64(v), 0).UTC().Format("2006-01-02 15:04")
		}
		revoked := ""
		if _, ok := t["revoked_at"]; ok {
			revoked = " (revoked)"
		}
		fmt.Printf("%-14s %-8s %-20s %-40s %s%s\n",
			t["prefix"], t["kind"], t["principal"], scopes, lastUsed, revoked)
	}
	return nil
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
	if _, err := tokensDelete(prof.Controller, prof.Token, "/api/v1/tokens/"+*prefix); err != nil {
		return err
	}
	fmt.Printf("revoked %s\n", *prefix)
	return nil
}

func runTokensLookup(args []string) error {
	fs := flag.NewFlagSet(cmdTokensLookup.Path, flag.ContinueOnError)
	on := addProfileFlag(fs)
	prefix := fs.String("prefix", "", "non-secret token prefix")
	if err := parseAndCheck(cmdTokensLookup, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "tokens lookup"); err != nil {
		return err
	}
	resp, err := tokensGet(prof.Controller, prof.Token, "/api/v1/tokens/"+*prefix)
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
	grace := fs.Duration("grace", 24*time.Hour, "window during which the old token still authenticates")
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
	body := map[string]any{
		"grace_secs": int64((*grace).Seconds()),
	}
	if *ttl > 0 {
		body["ttl_secs"] = int64((*ttl).Seconds())
	}
	resp, err := tokensPost(prof.Controller, prof.Token, "/api/v1/tokens/"+*prefix+"/rotate", body)
	if err != nil {
		return err
	}
	var out struct {
		Token       string          `json:"token"`
		New         json.RawMessage `json:"new"`
		OldRevoked  int64           `json:"old_revoked_at"`
		OldReplaced string          `json:"old_replaced_by"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "WARNING: stash this new token NOW. The old token continues working until the grace window closes.")
	fmt.Println(out.Token)
	fmt.Fprintf(os.Stderr, "---\nold prefix=%s revoked_at=%d\nnew metadata: %s\n",
		*prefix, out.OldRevoked, string(out.New))
	return nil
}

// --- HTTP helpers ---

func tokensPost(controller, token, path string, body any) ([]byte, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(controller, "/")+path, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return doTokenReq(req)
}

func tokensGet(controller, token, path string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(controller, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return doTokenReq(req)
}

func tokensDelete(controller, token, path string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodDelete, strings.TrimRight(controller, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return doTokenReq(req)
}

func doTokenReq(req *http.Request) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%s %s -> %d: %s", req.Method, req.URL.Path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// --- misc helpers ---

type urlQuery struct {
	parts []string
}

func url(_ string) urlQuery { return urlQuery{} }

func (q urlQuery) with(k, v string) urlQuery {
	return urlQuery{parts: append(q.parts, k+"="+v)}
}

func (q urlQuery) encode() string {
	if len(q.parts) == 0 {
		return ""
	}
	return "?" + strings.Join(q.parts, "&")
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

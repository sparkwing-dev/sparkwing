package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/discovery"
	"github.com/sparkwing-dev/sparkwing/internal/health"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: what an operator's own credential needs to dispatch work and watch it.
// Minting admin here would hand every workstation the tokens and secrets CRUD.
var cloudUserTokenScopes = []string{
	controller.ScopeRunsRead,
	controller.ScopeRunsWrite,
	controller.ScopeTriggersRead,
	controller.ScopeLogsRead,
	controller.ScopeApprovalsWrite,
}

func runCloud(args []string) error {
	if handleParentHelp(cmdCloud, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdCloud, os.Stderr)
		return errors.New("cloud: subcommand required (connect|disconnect|status)")
	}
	switch args[0] {
	case "connect":
		return runCloudConnect(args[1:])
	case "disconnect":
		return runCloudDisconnect(args[1:])
	case "status":
		return runCloudStatus(args[1:])
	default:
		PrintHelp(cmdCloud, os.Stderr)
		return fmt.Errorf("cloud: unknown subcommand %q", args[0])
	}
}

func runCloudConnect(args []string) error {
	fs := flag.NewFlagSet(cmdCloudConnect.Path, flag.ContinueOnError)
	controllerURL := fs.String("controller", "", "controller base URL")
	nameFlag := fs.String("name", "", "profile name (default: derived from the controller host)")
	tokenStdin := fs.Bool("token-stdin", false, "read an already-minted user token from stdin")
	adminStdin := fs.Bool("admin-token-stdin", false, "read an admin token from stdin and mint a user token with it")
	scopes := fs.String("scope", strings.Join(cloudUserTokenScopes, ","), "comma-separated scopes for the minted token")
	setDefault := fs.Bool("set-default", false, "set defaults.profile in this project's .sparkwing/sparkwing.yaml")
	force := fs.Bool("force", false, "replace an existing profile of that name")
	if err := parseAndCheck(cmdCloudConnect, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	base, err := normalizeControllerURL(*controllerURL)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(*nameFlag)
	if name == "" {
		name = profileNameForController(base)
	}

	cfg, path, err := loadCfg()
	if err != nil {
		return err
	}
	// safety: this refusal runs before the mint, so a rerun strands no live credential.
	if _, existed := cfg.Profiles[name]; existed && !*force {
		return fmt.Errorf("cloud connect: profile %q already exists in %s; pass --force to replace it, or --name to connect under another name",
			name, displayConfigPath(path))
	}
	var defaultsPath string
	if *setDefault {
		if defaultsPath, err = projectConfigPath(); err != nil {
			return err
		}
	}

	supplied := ""
	if *tokenStdin || *adminStdin {
		label := fmt.Sprintf("user token for %q", name)
		if *adminStdin {
			label = fmt.Sprintf("admin token to mint %q with", name)
		}
		if supplied, err = readTokenStdin(os.Stdin, os.Stderr, label); err != nil {
			return err
		}
		if supplied == "" {
			return errors.New("cloud connect: stdin carried no token")
		}
	}

	prof := &profile.Profile{Name: name, Controller: &profile.ControllerSpec{URL: base}}
	if !*adminStdin {
		prof.Controller.Token = supplied
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if reach := probeController(ctx, prof); reach.Status == "fail" {
		return fmt.Errorf("cloud connect: %s is not answering: %s", base, reach.Detail)
	}
	if supplied == "" && controllerEnforcesAuth(ctx, base) {
		return fmt.Errorf("cloud connect: %s authenticates every request; pass --admin-token-stdin to mint a token, or --token-stdin to store one you already hold", base)
	}

	if *adminStdin {
		minted, err := mintCloudUserToken(base, supplied, name, splitCSV(*scopes))
		if err != nil {
			return err
		}
		prof.Controller.Token = minted.Raw
		fmt.Printf("minted user token %s for %s\n", minted.Prefix, name)
	}

	cfg.Profiles[name] = prof
	if err := profile.Save(path, cfg); err != nil {
		return err
	}
	fmt.Printf("connected profile %q to %s (%s)\n", name, base, displayConfigPath(path))

	if defaultsPath != "" {
		if err := projectconfig.SetDefaultProfile(defaultsPath, name); err != nil {
			return err
		}
		fmt.Printf("set defaults.profile: %s in %s\n", name, defaultsPath)
	}

	printAnnouncedDashboard(ctx, prof)
	fmt.Println()
	report := probeProfile(ctx, prof)
	if err := writeProbeTable(os.Stdout, report); err != nil {
		return err
	}
	if !report.OK {
		return errors.New("the profile is written, but one or more probes failed")
	}
	return nil
}

func runCloudDisconnect(args []string) error {
	fs := flag.NewFlagSet(cmdCloudDisconnect.Path, flag.ContinueOnError)
	nameFlag := fs.String("name", "", "profile name to disconnect")
	adminStdin := fs.Bool("admin-token-stdin", false, "read an admin token from stdin and revoke with it")
	keepToken := fs.Bool("keep-token", false, "remove the profile without revoking its token")
	if err := parseAndCheck(cmdCloudDisconnect, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	name := strings.TrimSpace(*nameFlag)
	if name == "" {
		return errors.New("cloud disconnect: --name is required")
	}
	cfg, path, err := loadCfg()
	if err != nil {
		return err
	}
	prof, ok := cfg.Profiles[name]
	if !ok || prof == nil {
		return fmt.Errorf("cloud disconnect: %q not found in %s", name, displayConfigPath(path))
	}
	revokeWith := prof.ControllerToken()
	if *adminStdin {
		if revokeWith, err = readTokenStdin(os.Stdin, os.Stderr, fmt.Sprintf("admin token to revoke %q with", name)); err != nil {
			return err
		}
	}
	if !*keepToken {
		revokeCloudToken(prof, revokeWith)
	}

	delete(cfg.Profiles, name)
	if err := profile.Save(path, cfg); err != nil {
		return err
	}
	fmt.Printf("disconnected profile %q from %s\n", name, prof.ControllerURL())
	return nil
}

// safety: a revoke this credential is not allowed to make must not strand the
// operator, so the failure names the prefix and the command that finishes the job.
func revokeCloudToken(prof *profile.Profile, revokeWith string) {
	raw := prof.ControllerToken()
	if raw == "" || prof.ControllerURL() == "" {
		return
	}
	prefix, err := tokenPrefix(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "profile %q holds no revocable token: %v\n", prof.Name, err)
		return
	}
	if err := requireCloudUserToken(prof.ControllerURL(), revokeWith, prefix); err != nil {
		fmt.Fprintf(os.Stderr, "leaving the token %s live: %v\n", prefix, err)
		fmt.Fprintf(os.Stderr, "revoke it from an admin profile with:\n  sparkwing cluster tokens revoke --profile ADMIN --prefix %s\n", prefix)
		return
	}
	if _, err := tokensDelete(prof.ControllerURL(), revokeWith, "/api/v1/tokens/"+prefix); err != nil {
		fmt.Fprintf(os.Stderr, "leaving the token %s live: %v\n", prefix, err)
		fmt.Fprintf(os.Stderr, "revoke it from an admin profile with:\n  sparkwing cluster tokens revoke --profile ADMIN --prefix %s\n", prefix)
		return
	}
	fmt.Printf("revoked %s on %s\n", prefix, prof.ControllerURL())
}

// safety: the profile names a prefix, not a promise about it; revoking a runner
// or service token here would cut a machine off its controller.
func requireCloudUserToken(controllerURL, adminToken, prefix string) error {
	resp, err := tokensGet(controllerURL, adminToken, "/api/v1/tokens/"+prefix)
	if err != nil {
		return fmt.Errorf("look up the token %s: %w", prefix, err)
	}
	var found tokenListItem
	if err := json.Unmarshal(resp, &found); err != nil {
		return fmt.Errorf("decode the token %s: %w", prefix, err)
	}
	if found.Kind != store.TokenKindUser {
		return fmt.Errorf("the token %s is a %s token held by %q, not a user token", prefix, found.Kind, found.Principal)
	}
	return nil
}

func runCloudStatus(args []string) error {
	fs := flag.NewFlagSet(cmdCloudStatus.Path, flag.ContinueOnError)
	on := addProfileFlag(fs)
	outputFormat := fs.StringP("output", "o", "", "output format (json|table)")
	if err := parseAndCheck(cmdCloudStatus, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "cloud status"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	status := cloudStatusReport{
		Profile:    prof.Name,
		Controller: prof.ControllerURL(),
		Principal:  "(unauthenticated)",
	}
	if who, err := fetchWhoami(ctx, prof); err == nil {
		status.Principal = who.Principal
		status.Scopes = sortedScopes(who.Scopes)
		status.TokenPrefix = who.TokenPrefix
	}
	if services, err := discovery.ServicesFor(ctx, prof.ControllerURL(), prof.ControllerToken()); err == nil {
		status.Dashboard = services.Dashboard
	}
	report := probeProfile(ctx, prof)
	status.Probes = report.Probes
	status.OK = report.OK

	if *outputFormat == "json" {
		if err := json.NewEncoder(os.Stdout).Encode(status); err != nil {
			return err
		}
		if !status.OK {
			return errors.New("one or more probes failed")
		}
		return nil
	}
	fmt.Printf("profile:    %s\n", status.Profile)
	fmt.Printf("controller: %s\n", status.Controller)
	fmt.Printf("principal:  %s\n", status.Principal)
	if len(status.Scopes) > 0 {
		fmt.Printf("scopes:     %s\n", strings.Join(status.Scopes, ","))
	}
	if status.Dashboard != "" {
		fmt.Printf("dashboard:  %s\n", status.Dashboard)
	}
	if err := writeProbeTable(os.Stdout, report); err != nil {
		return err
	}
	if !status.OK {
		return errors.New("one or more probes failed")
	}
	return nil
}

type cloudStatusReport struct {
	Profile     string               `json:"profile"`
	Controller  string               `json:"controller"`
	Principal   string               `json:"principal"`
	Scopes      []string             `json:"scopes,omitempty"`
	TokenPrefix string               `json:"token_prefix,omitempty"`
	Dashboard   string               `json:"dashboard,omitempty"`
	Probes      []profileProbeResult `json:"probes"`
	OK          bool                 `json:"ok"`
}

func mintCloudUserToken(controllerURL, adminToken, principal string, scopes []string) (mintedToken, error) {
	if len(scopes) == 0 {
		return mintedToken{}, errors.New("cloud connect: --scope selected no scopes")
	}
	resp, err := tokensPost(controllerURL, adminToken, "/api/v1/tokens", map[string]any{
		"kind":      store.TokenKindUser,
		"principal": principal,
		"scopes":    scopes,
	})
	if err != nil {
		return mintedToken{}, err
	}
	var out struct {
		Token    string `json:"token"`
		Metadata struct {
			Prefix string `json:"prefix"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return mintedToken{}, fmt.Errorf("decode minted token: %w", err)
	}
	if out.Token == "" {
		return mintedToken{}, errors.New("the controller returned no token")
	}
	minted := mintedToken{Raw: out.Token, Prefix: out.Metadata.Prefix}
	if minted.Prefix == "" {
		if minted.Prefix, err = tokenPrefix(out.Token); err != nil {
			return mintedToken{}, err
		}
	}
	return minted, nil
}

func printAnnouncedDashboard(ctx context.Context, prof *profile.Profile) {
	services, err := discovery.ServicesFor(ctx, prof.ControllerURL(), prof.ControllerToken())
	switch {
	case err != nil:
		fmt.Printf("dashboard:  unknown (%v)\n", err)
	case services.Dashboard == "":
		fmt.Printf("dashboard:  this controller announces none; watch runs with `sparkwing runs list --profile %s`\n", prof.Name)
	default:
		fmt.Printf("dashboard:  %s\n", services.Dashboard)
	}
}

// safety: a tokenless profile against an authenticated controller reaches
// nothing, so connect refuses it. A controller whose health predates the auth
// field reports nothing, and an unreadable one already failed the reach probe.
func controllerEnforcesAuth(ctx context.Context, base string) bool {
	resp, err := httpGetNoAuth(ctx, strings.TrimRight(base, "/")+"/api/v1/health")
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := health.Decode(resp.Body)
	if err != nil {
		return false
	}
	return body.Auth != "" && body.Auth != "disabled"
}

func probeProfile(ctx context.Context, prof *profile.Profile) profileTestReport {
	report := profileTestReport{Profile: prof.Name, OK: true}
	report.Probes = append(report.Probes,
		probeController(ctx, prof),
		probeAuth(ctx, prof),
		probeLogs(ctx, prof),
		probeGitcache(ctx, prof))
	for _, p := range report.Probes {
		if p.Status == "fail" {
			report.OK = false
		}
	}
	return report
}

func normalizeControllerURL(raw string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := neturl.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("cloud connect: --controller %q does not parse: %w", raw, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("cloud connect: --controller %q must be an http or https URL, for example https://api.sparkwing.example", raw)
	}
	return trimmed, nil
}

func profileNameForController(controllerURL string) string {
	u, err := neturl.Parse(controllerURL)
	if err != nil {
		return "cloud"
	}
	var b strings.Builder
	for _, r := range strings.ToLower(u.Hostname()) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		return "cloud"
	}
	return name
}

func projectConfigPath() (string, error) {
	dir, err := findSparkwingDir()
	if err != nil {
		return "", fmt.Errorf("cloud connect: --set-default needs a sparkwing project: %w", err)
	}
	return filepath.Join(dir, projectconfig.Filename), nil
}

func writeProbeTable(w io.Writer, report profileTestReport) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, p := range report.Probes {
		latency := ""
		if p.LatencyMS > 0 {
			latency = fmt.Sprintf("(%dms)", p.LatencyMS)
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n",
			p.Name, p.Status, orDash(p.Target), strings.TrimSpace(p.Detail), latency)
	}
	return tw.Flush()
}

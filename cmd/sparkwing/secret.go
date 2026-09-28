package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func runSecret(args []string) error {
	if handleParentHelp(cmdSecret, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdSecret, os.Stderr)
		return errors.New("secret: subcommand required (set|get|list|delete|rotate)")
	}
	switch args[0] {
	case "set":
		return runSecretSet(args[1:])
	case "get":
		return runSecretGet(args[1:])
	case "list":
		return runSecretList(args[1:])
	case "delete", "rm", "remove":
		return runSecretDelete(args[1:])
	case "rotate":
		return runSecretRotate(args[1:])
	default:
		PrintHelp(cmdSecret, os.Stderr)
		return fmt.Errorf("secret: unknown subcommand %q", args[0])
	}
}

type secretsTarget struct {
	c     *client.Client
	label string
	local bool
	// safety: a store that does not exist yet holds no secret, so a read answers without asking.
	empty bool
	close func()
}

func openSecretsTarget(fs *flag.FlagSet, on, verb string, write bool) (*secretsTarget, error) {
	if fs.Changed("profile") {
		prof, err := resolveProfile(on)
		if err != nil {
			return nil, err
		}
		if err := requireController(prof, verb); err != nil {
			return nil, err
		}
		return &secretsTarget{
			c:     client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken()),
			label: "on: " + prof.Name,
			close: func() {},
		}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), ensureDaemonTimeout)
	defer cancel()
	return localSecretsTarget(ctx, verb, write)
}

// hack: a variable so a test can host the daemon in its own process.
var secretsDaemonOptions = func() wingdclient.Options {
	return wingdclient.Options{Version: installedVersion()}
}

func localSecretsTarget(ctx context.Context, verb string, write bool) (*secretsTarget, error) {
	cl, err := wingdclient.EnsureDaemon(ctx, secretsDaemonOptions())
	if err != nil {
		return nil, fmt.Errorf("%s: this machine's sparkwing daemon serves the local secret store, and it did not start: %w", verb, err)
	}
	daemon := cl.DaemonVersion()
	ready, apiErr, sock := cl.APIReady(), cl.APIError(), cl.APISocket()
	if err := cl.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: closing the daemon connection: %v\n", verb, err)
	}
	if !ready {
		if apiErr == "" {
			apiErr = "it serves no controller API"
		}
		return nil, fmt.Errorf("%s: this machine's sparkwing daemon (%s) cannot serve the local secret store: %s; "+
			"run `sparkwing daemon restart` to start this release's daemon", verb, daemon, apiErr)
	}
	httpClient := orchestrator.NewAPISocketClient(sock)
	health, err := orchestrator.ReadLocalAPIHealth(ctx, sock)
	if err != nil {
		httpClient.CloseIdleConnections()
		return nil, fmt.Errorf("%s: %w", verb, err)
	}
	// safety: a daemon that predates local encryption would store the value
	// as plaintext in state.db, so a write is refused rather than sent to it.
	if write && health.Secrets != orchestrator.APISecretsSealed {
		httpClient.CloseIdleConnections()
		return nil, fmt.Errorf("%s: this machine's sparkwing daemon (%s) predates encrypted local secrets and would store the value "+
			"unencrypted; run `sparkwing daemon restart` to start this release's daemon, then run this again", verb, daemon)
	}
	if health.SecretsProblem != "" {
		httpClient.CloseIdleConnections()
		return nil, fmt.Errorf("%s: %s", verb, health.SecretsProblem)
	}
	return &secretsTarget{
		c:     client.New(orchestrator.HostedAPIBaseURL, httpClient),
		label: "local",
		local: true,
		empty: health.Store == "absent",
		close: httpClient.CloseIdleConnections,
	}, nil
}

func runSecretSet(args []string) error {
	fs := flag.NewFlagSet(cmdSecretSet.Path, flag.ContinueOnError)
	v := bindFlags(cmdSecretSet, fs)
	if err := parseAndCheck(cmdSecretSet, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	name := v.String("name")
	value := v.String("value")
	file := v.String("file")
	plain := v.Bool("plain")
	pipeline := v.String("pipeline")
	shared := v.Bool("shared")
	if !fs.Changed("value") && !fs.Changed("file") {
		return errors.New("secret set: either --value or --file is required")
	}
	if name == "" {
		return errors.New("secret set: --name is required")
	}
	if err := secrets.ValidateName(name); err != nil {
		return fmt.Errorf("secret set: %w", err)
	}
	if shared && pipeline != "" {
		return errors.New("secret set: --shared and --pipeline are exclusive; a pipeline's secret is already scoped")
	}

	raw := value
	if fs.Changed("file") {
		data, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("secret set: read %s: %w", file, err)
		}
		raw = string(data)
	}
	masked := !plain

	target, err := openSecretsTarget(fs, v.String("profile"), "secret set", true)
	if err != nil {
		return err
	}
	defer target.close()
	// safety: the machine's own runs read every local row, so a local secret
	// is stored the way the import and the dashboard store one, shared,
	// rather than as an admin-only row a run would read only by that gap.
	if target.local && pipeline == "" {
		shared = true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := target.c.CreateSecretForPipeline(ctx, name, raw, pipeline, masked, shared); err != nil {
		return fmt.Errorf("secret set: %w", err)
	}
	scope := "admin only"
	switch {
	case pipeline != "":
		scope = pipeline
	case shared:
		scope = "every pipeline"
	}
	fmt.Fprintf(os.Stdout, "secret %q set (%s, pipeline: %s, masked=%v)\n", name, target.label, scope, masked)
	return nil
}

func runSecretGet(args []string) error {
	fs := flag.NewFlagSet(cmdSecretGet.Path, flag.ContinueOnError)
	v := bindFlags(cmdSecretGet, fs)
	if err := parseAndCheck(cmdSecretGet, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	name := v.String("name")
	pipeline := v.String("pipeline")
	if name == "" {
		return errors.New("secret get: --name is required")
	}
	target, err := openSecretsTarget(fs, v.String("profile"), "secret get", false)
	if err != nil {
		return err
	}
	defer target.close()
	if target.empty {
		return fmt.Errorf("secret get: %q not found (%s)", name, target.label)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sec, err := target.c.GetSecretForPipeline(ctx, name, pipeline)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("secret get: %q not found (%s)", name, target.label)
		}
		return fmt.Errorf("secret get: %w", err)
	}
	fmt.Fprint(os.Stdout, sec.Value)
	return nil
}

func runSecretList(args []string) error {
	fs := flag.NewFlagSet(cmdSecretList.Path, flag.ContinueOnError)
	v := bindFlags(cmdSecretList, fs)
	if err := parseAndCheck(cmdSecretList, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	grep := v.String("grep")
	target, err := openSecretsTarget(fs, v.String("profile"), "secret list", false)
	if err != nil {
		return err
	}
	defer target.close()
	var secs []client.Secret
	if !target.empty {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		secs, err = target.c.ListSecrets(ctx)
		if err != nil {
			return fmt.Errorf("secret list: %w", err)
		}
	}
	if grep != "" {
		filtered := secs[:0]
		for _, s := range secs {
			if strings.Contains(s.Name, grep) {
				filtered = append(filtered, s)
			}
		}
		secs = filtered
	}
	if len(secs) == 0 {
		fmt.Fprintf(os.Stdout, "(no secrets, %s)\n", target.label)
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPIPELINE\tMASKED\tBOUND\tPRINCIPAL\tCREATED\tUPDATED")
	for _, sec := range secs {
		scope := sec.Pipeline
		if scope == "" {
			scope = "(admin only)"
			if sec.Shared {
				scope = "(every pipeline)"
			}
		}
		fmt.Fprintf(
			tw, "%s\t%s\t%v\t%v\t%s\t%s\t%s\n",
			sec.Name, scope, sec.Masked, sec.Bound, sec.Principal,
			time.Unix(sec.CreatedAt, 0).UTC().Format("2006-01-02 15:04"),
			time.Unix(sec.UpdatedAt, 0).UTC().Format("2006-01-02 15:04"),
		)
	}
	return tw.Flush()
}

func runSecretDelete(args []string) error {
	fs := flag.NewFlagSet(cmdSecretDelete.Path, flag.ContinueOnError)
	v := bindFlags(cmdSecretDelete, fs)
	if err := parseAndCheck(cmdSecretDelete, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	name := v.String("name")
	pipeline := v.String("pipeline")
	if name == "" {
		return errors.New("secret delete: --name is required")
	}
	target, err := openSecretsTarget(fs, v.String("profile"), "secret delete", false)
	if err != nil {
		return err
	}
	defer target.close()
	if target.empty {
		return fmt.Errorf("secret delete: %q not found (%s)", name, target.label)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := target.c.DeleteSecretForPipeline(ctx, name, pipeline); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("secret delete: %q not found (%s)", name, target.label)
		}
		return fmt.Errorf("secret delete: %w", err)
	}
	fmt.Fprintf(os.Stdout, "secret %q deleted (%s)\n", name, target.label)
	return nil
}

func runSecretRotate(args []string) error {
	fs := flag.NewFlagSet(cmdSecretRotate.Path, flag.ContinueOnError)
	v := bindFlags(cmdSecretRotate, fs)
	if err := parseAndCheck(cmdSecretRotate, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	target, err := openSecretsTarget(fs, v.String("profile"), "secret rotate", true)
	if err != nil {
		return err
	}
	defer target.close()
	if target.empty {
		fmt.Fprintf(os.Stdout, "0 secret(s) re-encrypted under the current key (%s)\n", target.label)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	result, err := target.c.RotateSecrets(ctx)
	if err != nil {
		return fmt.Errorf("secret rotate: %w", err)
	}
	fmt.Fprintf(os.Stdout,
		"%d secret(s) re-encrypted under the current key (%s)\n", result.Rotated, target.label)
	if len(result.Skipped) == 0 {
		fmt.Fprintln(os.Stdout, "the previous key can now be dropped")
		return nil
	}
	fmt.Fprintf(os.Stdout,
		"%d secret(s) opened under no configured key and were left as they are:\n", len(result.Skipped))
	for _, skip := range result.Skipped {
		if skip.Pipeline == "" {
			fmt.Fprintf(os.Stdout, "  %s\n", skip.Name)
			continue
		}
		fmt.Fprintf(os.Stdout, "  %s (pipeline %s)\n", skip.Name, skip.Pipeline)
	}
	fmt.Fprintln(os.Stdout,
		"re-set those secrets, or name the key they were sealed under, before dropping the previous key")
	return nil
}

// `sparkwing secrets` subcommand. CRUD over secret stores. Two modes:
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/v2/controller/client"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/v2/secrets"
)

func runSecret(args []string) error {
	if handleParentHelp(cmdSecret, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdSecret, os.Stderr)
		return errors.New("secret: subcommand required (set|get|list|delete)")
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
	default:
		PrintHelp(cmdSecret, os.Stderr)
		return fmt.Errorf("secret: unknown subcommand %q", args[0])
	}
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
	on := v.String("on")
	if !fs.Changed("value") && !fs.Changed("file") {
		return errors.New("secret set: either --value or --file is required")
	}
	if name == "" {
		return errors.New("secret set: --name is required")
	}
	if err := secrets.ValidateName(name); err != nil {
		return fmt.Errorf("secret set: %w", err)
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

	if !fs.Changed("on") {
		// Local: pick the file by mask intent. Plain values go to
		// config.env so an operator can chmod / share / version-
		// control them independently of the (still-0600) secrets
		// file. WriteDotenvEntry handles the chmod 0600 either way
		// -- there's no harm in tightening config.env too, and it
		// keeps the write path uniform.
		path, perr := localPathFor(masked)
		if perr != nil {
			return fmt.Errorf("secret set: %w", perr)
		}
		if err := secrets.WriteDotenvEntry(path, name, raw); err != nil {
			return fmt.Errorf("secret set: %w", err)
		}
		// Prevent collision the other direction: if the operator
		// flipped a name's masked flag (was masked, now plain), the
		// stale row in the other file will keep winning until they
		// remove it. Strip the duplicate proactively.
		other, oerr := localPathFor(!masked)
		if oerr == nil {
			_ = secrets.DeleteDotenvEntry(other, name)
		}
		fmt.Fprintf(os.Stdout, "secret %q set (local: %s, masked=%v)\n", name, path, masked)
		return nil
	}

	prof, err := resolveProfile(on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "secret set"); err != nil {
		return err
	}
	c := client.NewWithToken(prof.Controller, nil, prof.Token)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.CreateSecret(ctx, name, raw, masked); err != nil {
		return fmt.Errorf("secret set: %w", err)
	}
	fmt.Fprintf(os.Stdout, "secret %q set (on: %s, masked=%v)\n", name, prof.Name, masked)
	return nil
}

// localPathFor returns the local dotenv file for the given mask
// intent. Centralized so set / delete / list pick the right side
// consistently.
func localPathFor(masked bool) (string, error) {
	if masked {
		return secrets.DefaultDotenvPath()
	}
	return secrets.DefaultConfigPath()
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
	on := v.String("on")
	if name == "" {
		return errors.New("secret get: --name is required")
	}

	if !fs.Changed("on") {
		src := secrets.NewDotenvSource("")
		val, _, err := src.Read(name)
		if err != nil {
			if errors.Is(err, secrets.ErrSecretMissing) {
				return fmt.Errorf("secret get: %q not set in local store (%s)", name, src.Path())
			}
			return fmt.Errorf("secret get: %w", err)
		}
		fmt.Fprint(os.Stdout, val)
		return nil
	}

	prof, err := resolveProfile(on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "secret get"); err != nil {
		return err
	}
	c := client.NewWithToken(prof.Controller, nil, prof.Token)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sec, err := c.GetSecret(ctx, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("secret get: %q not found", name)
		}
		return fmt.Errorf("secret get: %w", err)
	}
	// No trailing newline so the raw value pipes cleanly (e.g.
	// `sparkwing secrets get --name X | docker login --password-stdin`).
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
	on := v.String("on")
	grep := v.String("grep")

	if !fs.Changed("on") {
		// Read both files: secrets.env (masked) and config.env (plain).
		// On collision, plain wins -- mirrors DotenvSource.Read so
		// what the list shows is what jobs will see.
		secretsPath, _ := secrets.DefaultDotenvPath()
		configPath, _ := secrets.DefaultConfigPath()
		maskedEntries, err := secrets.ListDotenvEntries(secretsPath)
		if err != nil {
			return fmt.Errorf("secret list: %w", err)
		}
		plainEntries, err := secrets.ListDotenvEntries(configPath)
		if err != nil {
			return fmt.Errorf("secret list: %w", err)
		}
		type row struct {
			name   string
			masked bool
			path   string
		}
		rowByName := map[string]row{}
		for k := range maskedEntries {
			if grep != "" && !strings.Contains(k, grep) {
				continue
			}
			rowByName[k] = row{name: k, masked: true, path: secretsPath}
		}
		for k := range plainEntries {
			if grep != "" && !strings.Contains(k, grep) {
				continue
			}
			// Plain wins on collision -- intentional dup is treated
			// as the operator having flipped the entry's class.
			rowByName[k] = row{name: k, masked: false, path: configPath}
		}
		if len(rowByName) == 0 {
			fmt.Fprintf(os.Stdout, "(no secrets in %s or %s)\n", secretsPath, configPath)
			return nil
		}
		names := make([]string, 0, len(rowByName))
		for k := range rowByName {
			names = append(names, k)
		}
		sort.Strings(names)
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tMASKED\tSTORE")
		for _, k := range names {
			r := rowByName[k]
			fmt.Fprintf(tw, "%s\t%v\tlocal: %s\n", r.name, r.masked, r.path)
		}
		return tw.Flush()
	}

	prof, err := resolveProfile(on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "secret list"); err != nil {
		return err
	}
	c := client.NewWithToken(prof.Controller, nil, prof.Token)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	secs, err := c.ListSecrets(ctx)
	if err != nil {
		return fmt.Errorf("secret list: %w", err)
	}
	// Client-side name filter: secret counts are small (< dozens in
	// practice), so the extra hop to the controller for a server-side
	// filter isn't worth it. Keeps the surface narrow.
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
		fmt.Fprintln(os.Stdout, "(no secrets)")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tMASKED\tPRINCIPAL\tCREATED\tUPDATED")
	for _, sec := range secs {
		fmt.Fprintf(tw, "%s\t%v\t%s\t%s\t%s\n",
			sec.Name, sec.Masked, sec.Principal,
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
	on := v.String("on")
	if name == "" {
		return errors.New("secret delete: --name is required")
	}

	if !fs.Changed("on") {
		// Try both files; either may hold the entry. At least one
		// must hit -- otherwise the name is genuinely absent.
		secretsPath, _ := secrets.DefaultDotenvPath()
		configPath, _ := secrets.DefaultConfigPath()
		removedFrom := ""
		if err := secrets.DeleteDotenvEntry(secretsPath, name); err == nil {
			removedFrom = secretsPath
		} else if !errors.Is(err, secrets.ErrSecretMissing) {
			return fmt.Errorf("secret delete: %w", err)
		}
		if err := secrets.DeleteDotenvEntry(configPath, name); err == nil {
			if removedFrom != "" {
				removedFrom += " + " + configPath
			} else {
				removedFrom = configPath
			}
		} else if !errors.Is(err, secrets.ErrSecretMissing) {
			return fmt.Errorf("secret delete: %w", err)
		}
		if removedFrom == "" {
			return fmt.Errorf("secret delete: %q not set in local store", name)
		}
		fmt.Fprintf(os.Stdout, "secret %q deleted (local: %s)\n", name, removedFrom)
		return nil
	}

	prof, err := resolveProfile(on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "secret delete"); err != nil {
		return err
	}
	c := client.NewWithToken(prof.Controller, nil, prof.Token)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.DeleteSecret(ctx, name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("secret delete: %q not found", name)
		}
		return fmt.Errorf("secret delete: %w", err)
	}
	fmt.Fprintf(os.Stdout, "secret %q deleted (on: %s)\n", name, prof.Name)
	return nil
}

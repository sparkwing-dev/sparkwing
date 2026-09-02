package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	flag "github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/sparkwing-dev/sparkwing/internal/ndjson"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
)

func runProfiles(args []string) error {
	if handleParentHelp(cmdProfiles, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdProfiles, os.Stderr)
		return errors.New("profiles: subcommand required")
	}
	switch args[0] {
	case "add":
		return runProfilesAdd(args[1:])
	case "list", "ls":
		return runProfilesList(args[1:])
	case "show":
		return runProfilesShow(args[1:])
	case "remove", "rm", "delete":
		return runProfilesRemove(args[1:])
	case "duplicate", "dup":
		return runProfilesDuplicate(args[1:])
	case "set":
		return runProfilesSet(args[1:])
	case "test":
		return runProfilesTest(args[1:])
	default:
		PrintHelp(cmdProfiles, os.Stderr)
		return fmt.Errorf("profiles: unknown subcommand %q", args[0])
	}
}

func loadCfg() (*profile.Config, string, error) {
	path, err := profile.DefaultPath()
	if err != nil {
		return nil, "", err
	}
	cfg, err := profile.Load(path)
	if err != nil {
		return nil, path, err
	}
	return cfg, path, nil
}

func runProfilesAdd(args []string) error {
	fs := flag.NewFlagSet(cmdProfilesAdd.Path, flag.ContinueOnError)
	name := fs.String("name", "", "profile name (unique per profiles.yaml)")
	controller := fs.String("controller", "", "controller base URL (required for remote dispatch)")
	token := fs.String("token", "", "bearer token, visible to other processes in the process list and shell history (optional -- omit for unauthed controllers)")
	tokenStdin := fs.Bool("token-stdin", false, "read the bearer token from stdin, prompting without echo when stdin is a terminal")
	if err := parseAndCheck(cmdProfilesAdd, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}

	cfg, path, err := loadCfg()
	if err != nil {
		return err
	}
	if _, existed := cfg.Profiles[*name]; existed {
		return fmt.Errorf("profiles add: %q already exists (use `profiles remove` first, or `profiles duplicate` into a new name)", *name)
	}
	if *tokenStdin {
		read, err := readTokenStdin(os.Stdin, os.Stderr, fmt.Sprintf("bearer token for %q", *name))
		if err != nil {
			return err
		}
		*token = read
	}
	p := &profile.Profile{Name: *name}
	if *controller != "" || *token != "" {
		p.Controller = &profile.ControllerSpec{URL: *controller, Token: *token}
	}
	cfg.Profiles[*name] = p
	if err := profile.Save(path, cfg); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "added profile %q at %s\n", *name, path)
	return nil
}

type profileIndex struct {
	Name       string `json:"name"`
	Controller string `json:"controller,omitempty"`
	Logs       string `json:"logs,omitempty"`
	Token      string `json:"token,omitempty"`
}

func runProfilesList(args []string) error {
	fs := flag.NewFlagSet(cmdProfilesList.Path, flag.ContinueOnError)
	var output string
	fs.StringVarP(&output, "output", "o", "", "pretty | json | plain")
	if err := parseAndCheck(cmdProfilesList, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	format, err := resolveOutputFormat(output, cmdProfilesList.Path)
	if err != nil {
		return err
	}
	cfg, path, err := loadCfg()
	if err != nil {
		return err
	}

	if len(cfg.Profiles) == 0 {
		fmt.Fprintln(os.Stderr, "no profiles configured")
		fmt.Fprintf(os.Stderr, "expected at %s -- register one with `sparkwing configure profiles add --name NAME --controller URL`\n", path)
		return nil
	}
	index := make([]profileIndex, 0, len(cfg.Profiles))
	for _, name := range cfg.Names() {
		p := cfg.Profiles[name]
		index = append(index, profileIndex{
			Name:       name,
			Controller: p.ControllerURL(),
			Logs:       profile.SpecString(p.Logs),
			Token:      redactToken(p.ControllerToken()),
		})
	}
	switch format {
	case "json":
		return ndjson.Write(os.Stdout, index)
	case "plain":
		for _, p := range index {
			fmt.Println(p.Name)
		}
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tCONTROLLER\tLOGS\tTOKEN")
	for _, p := range index {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.Name, emptyDash(p.Controller), p.Logs, p.Token)
	}
	_ = tw.Flush()
	return nil
}

func runProfilesShow(args []string) error {
	fs := flag.NewFlagSet(cmdProfilesShow.Path, flag.ContinueOnError)
	nameFlag := fs.String("name", "", "profile name")
	showToken := fs.Bool("show-token", false, "print the raw token (redacted by default)")
	if err := parseAndCheck(cmdProfilesShow, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	cfg, _, err := loadCfg()
	if err != nil {
		return err
	}
	name := *nameFlag
	if name == "" {
		return errors.New("profiles show: pass --name NAME")
	}
	p, ok := cfg.Profiles[name]
	if !ok {
		return fmt.Errorf("profiles show: %q not found", name)
	}
	fmt.Fprintf(os.Stdout, "name:       %s\n", p.Name)
	fmt.Fprintf(os.Stdout, "controller: %s\n", p.ControllerURL())
	fmt.Fprintf(os.Stdout, "logs:       %s\n", profile.SpecString(p.Logs))
	if *showToken {
		fmt.Fprintf(os.Stdout, "token:      %s\n", emptyDash(p.ControllerToken()))
	} else {
		fmt.Fprintf(os.Stdout, "token:      %s\n", redactToken(p.ControllerToken()))
	}
	return nil
}

func runProfilesRemove(args []string) error {
	fs := flag.NewFlagSet(cmdProfilesRemove.Path, flag.ContinueOnError)
	nameFlag := fs.String("name", "", "profile name to remove")
	if err := parseAndCheck(cmdProfilesRemove, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	name := *nameFlag
	cfg, path, err := loadCfg()
	if err != nil {
		return err
	}
	if _, ok := cfg.Profiles[name]; !ok {
		return fmt.Errorf("profiles remove: %q not found", name)
	}
	delete(cfg.Profiles, name)
	if err := profile.Save(path, cfg); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "removed profile %q\n", name)
	return nil
}

func runProfilesSet(args []string) error {
	fs := flag.NewFlagSet(cmdProfilesSet.Path, flag.ContinueOnError)
	nameFlag := fs.String("name", "", "profile name to mutate")
	controller := fs.String("controller", "", "new controller URL")
	token := fs.String("token", "", "new bearer token, visible to other processes in the process list and shell history")
	tokenStdin := fs.Bool("token-stdin", false, "read the new bearer token from stdin, prompting without echo when stdin is a terminal")
	if err := parseAndCheck(cmdProfilesSet, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	name := *nameFlag
	cfg, path, err := loadCfg()
	if err != nil {
		return err
	}
	p, ok := cfg.Profiles[name]
	if !ok {
		return fmt.Errorf("profiles set: %q not found", name)
	}
	if *tokenStdin {
		read, err := readTokenStdin(os.Stdin, os.Stderr, fmt.Sprintf("new bearer token for %q", name))
		if err != nil {
			return err
		}
		*token = read
	}
	tokenGiven := fs.Changed("token") || *tokenStdin
	if fs.Changed("controller") || tokenGiven {
		if p.Controller == nil {
			p.Controller = &profile.ControllerSpec{}
		}
		if fs.Changed("controller") {
			p.Controller.URL = *controller
		}
		if tokenGiven {
			p.Controller.Token = *token
		}
	}
	if err := profile.Save(path, cfg); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "updated profile %q\n", name)
	return nil
}

func runProfilesDuplicate(args []string) error {
	fs := flag.NewFlagSet(cmdProfilesDuplicate.Path, flag.ContinueOnError)
	srcFlag := fs.String("src", "", "source profile name")
	dstFlag := fs.String("dst", "", "destination profile name (must not exist yet)")
	if err := parseAndCheck(cmdProfilesDuplicate, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	src := *srcFlag
	dst := *dstFlag
	if src == dst {
		return errors.New("profiles duplicate: SRC and DST must differ")
	}
	cfg, path, err := loadCfg()
	if err != nil {
		return err
	}
	p, ok := cfg.Profiles[src]
	if !ok {
		return fmt.Errorf("profiles duplicate: %q not found", src)
	}
	if _, exists := cfg.Profiles[dst]; exists {
		return fmt.Errorf("profiles duplicate: %q already exists", dst)
	}
	cp := *p
	cp.Name = dst
	cfg.Profiles[dst] = &cp
	if err := profile.Save(path, cfg); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "duplicated %q -> %q (inspect it with `sparkwing configure profiles show --name %s`, then edit profiles.yaml or remove and re-add it)\n", src, dst, dst)
	return nil
}

func readTokenStdin(in *os.File, prompt io.Writer, label string) (string, error) {
	if term.IsTerminal(int(in.Fd())) {
		fmt.Fprintf(prompt, "%s: ", label)
		raw, err := term.ReadPassword(int(in.Fd()))
		fmt.Fprintln(prompt)
		if err != nil {
			return "", fmt.Errorf("read token: %w", err)
		}
		return strings.TrimSpace(string(raw)), nil
	}
	raw, err := io.ReadAll(in)
	if err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

func redactToken(t string) string {
	if t == "" {
		return "(none)"
	}
	if len(t) <= 12 {
		return "***"
	}
	return t[:4] + "..." + strings.Repeat("*", 8)
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

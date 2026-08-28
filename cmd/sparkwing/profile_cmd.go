package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
)

func runProfileCmd(args []string) error {
	fs := flag.NewFlagSet(cmdProfile.Path, flag.ContinueOnError)
	profileName := fs.String("profile", "", "hypothetical: which profile would `--profile NAME` pick (default: the active no-flag resolution)")
	output := fs.StringP("output", "o", "pretty", "output format: pretty | json")
	if err := parseAndCheck(cmdProfile, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		PrintHelp(cmdProfile, os.Stderr)
		return fmt.Errorf("profile: unexpected positional %q (this verb takes no arguments; use --profile NAME for the hypothetical case)", fs.Arg(0))
	}
	format, err := resolveOutputFormat(*output, cmdProfile.Path)
	if err != nil {
		return err
	}

	p, chain, cfgPath, err := resolveProfileChain(*profileName)
	if err != nil {
		return err
	}

	if format == "json" {
		return renderProfileJSON(p, chain, os.Stdout)
	}
	return renderProfilePretty(p, chain, cfgPath, os.Stdout)
}

type profileEffectiveJSON struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	Controller  string `json:"controller"`
	State       string `json:"state"`
	Logs        string `json:"logs"`
	Cache       string `json:"cache"`
	MirrorLocal bool   `json:"mirror_local"`
}

type profileConsideredJSON struct {
	Source string `json:"source"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type profileReportJSON struct {
	Effective  profileEffectiveJSON    `json:"effective"`
	Considered []profileConsideredJSON `json:"considered"`
}

func renderProfileJSON(p *profile.Profile, chain profile.Chain, out io.Writer) error {
	state, logs, cache := p.SurfaceStrings()
	report := profileReportJSON{
		Effective: profileEffectiveJSON{
			Name:        chain.Selected,
			Source:      string(chain.Source),
			Controller:  p.ControllerURL(),
			State:       state,
			Logs:        logs,
			Cache:       cache,
			MirrorLocal: p.EffectiveMirrorLocal(),
		},
		Considered: chainRows(chain),
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func renderProfilePretty(p *profile.Profile, chain profile.Chain, cfgPath string, out io.Writer) error {
	state, logs, cache := p.SurfaceStrings()

	fmt.Fprintf(out, "effective profile: %s\n", chain.Selected)
	fmt.Fprintf(out, "  source:           %s\n", effectiveSourceDetail(chain, cfgPath))
	if p.ControllerURL() != "" {
		fmt.Fprintf(out, "  controller:       %s\n", p.ControllerURL())
	}
	fmt.Fprintf(out, "  state:            %s\n", state)
	fmt.Fprintf(out, "  logs:             %s\n", logs)
	fmt.Fprintf(out, "  cache:            %s\n", cache)
	fmt.Fprintf(out, "  mirror_local:     %t\n", p.EffectiveMirrorLocal())

	fmt.Fprintln(out)
	fmt.Fprintln(out, "resolution chain considered:")
	for _, row := range chainRows(chain) {
		if row.Source == string(chain.Source) {
			fmt.Fprintf(out, "  %-12s %s ← selected\n", row.Source, row.Name)
			continue
		}
		fmt.Fprintf(out, "  %-12s %s\n", row.Source, row.Reason)
	}
	return nil
}

func effectiveSourceDetail(chain profile.Chain, cfgPath string) string {
	switch chain.Source {
	case profile.ChainSourceFlag:
		return fmt.Sprintf("flag (--profile %s)", chain.Selected)
	case profile.ChainSourceProjectDefault:
		return fmt.Sprintf("no --profile; project defaults.profile: %s", chain.Selected)
	case profile.ChainSourceNone:
		return "no --profile, and the project names no default (built-in local defaults apply)"
	default:
		return fmt.Sprintf("%s (%s)", chain.Source, displayConfigPath(cfgPath))
	}
}

func chainRows(chain profile.Chain) []profileConsideredJSON {
	switch chain.Source {
	case profile.ChainSourceFlag:
		return []profileConsideredJSON{
			{Source: string(profile.ChainSourceFlag), Name: chain.Selected, Reason: "selected"},
		}
	case profile.ChainSourceProjectDefault:
		return []profileConsideredJSON{
			{Source: string(profile.ChainSourceProjectDefault), Name: chain.Selected, Reason: "selected"},
		}
	}
	return []profileConsideredJSON{
		{Source: string(profile.ChainSourceNone), Name: "", Reason: "no --profile passed and the project names no default"},
	}
}

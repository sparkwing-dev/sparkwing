package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
)

func showSelectedProfile(profileName, output string) error {
	format, err := resolveOutputFormat(output, cmdProfilesShow.Path)
	if err != nil {
		return err
	}

	p, chain, cfgPath, err := resolveProfileChain(profileName)
	if err != nil {
		return err
	}

	if format == "json" {
		return renderProfileJSON(p, chain, os.Stdout)
	}
	return renderProfilePretty(p, chain, cfgPath, os.Stdout)
}

type profileEffectiveJSON struct {
	Name       string `json:"name"`
	Source     string `json:"source"`
	Controller string `json:"controller"`
	State      string `json:"state"`
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
	report := profileReportJSON{
		Effective: profileEffectiveJSON{
			Name:       chain.Selected,
			Source:     string(chain.Source),
			Controller: p.ControllerURL(),
			State:      p.StateString(),
		},
		Considered: chainRows(chain),
	}
	enc := json.NewEncoder(out)
	return enc.Encode(report)
}

func renderProfilePretty(p *profile.Profile, chain profile.Chain, cfgPath string, out io.Writer) error {
	fmt.Fprintf(out, "effective profile: %s\n", chain.Selected)
	fmt.Fprintf(out, "  source:           %s\n", effectiveSourceDetail(chain, cfgPath))
	if p.ControllerURL() != "" {
		fmt.Fprintf(out, "  controller:       %s\n", p.ControllerURL())
	}
	fmt.Fprintf(out, "  state:            %s\n", p.StateString())

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
	case profile.ChainSourceNone:
		return "no --profile (this machine's local store applies)"
	default:
		return fmt.Sprintf("%s (%s)", chain.Source, displayConfigPath(cfgPath))
	}
}

func chainRows(chain profile.Chain) []profileConsideredJSON {
	if chain.Source == profile.ChainSourceFlag {
		return []profileConsideredJSON{
			{Source: string(profile.ChainSourceFlag), Name: chain.Selected, Reason: "selected"},
		}
	}
	return []profileConsideredJSON{
		{Source: string(profile.ChainSourceNone), Name: "", Reason: "no --profile passed"},
	}
}

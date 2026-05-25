// `sparkwing profile` is read-side introspection: it reports which
// profile a sparkwing command would pick right now, and why, using the
// same profile.ResolveChain machinery `sparkwing run` / `pipeline
// trigger` use. No execution side-effects.
//
// projectHint is passed as "" (matching step 5): the `.sparkwing/
// sparkwing.yaml` profile: field is not read here. Step 9 wires that
// hint into both the run flow AND this introspection at once, so this
// command never reports a project level the real commands don't honor.
// Until then the project row reads "not yet wired (step 9)".
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
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

// --- JSON output ---

type profileEffectiveJSON struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	DetectVia   string `json:"detect_via"`
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
	state, logs, cache := effectiveSurfaces(p)
	report := profileReportJSON{
		Effective: profileEffectiveJSON{
			Name:        chain.Selected,
			Source:      string(chain.Source),
			DetectVia:   chain.DetectVia,
			Controller:  p.Controller,
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

// --- pretty output ---

func renderProfilePretty(p *profile.Profile, chain profile.Chain, cfgPath string, out io.Writer) error {
	state, logs, cache := effectiveSurfaces(p)

	fmt.Fprintf(out, "effective profile: %s\n", chain.Selected)
	fmt.Fprintf(out, "  source:           %s\n", effectiveSourceDetail(chain, cfgPath))
	if p.Controller != "" {
		fmt.Fprintf(out, "  controller:       %s\n", p.Controller)
	}
	fmt.Fprintf(out, "  state:            %s\n", state)
	fmt.Fprintf(out, "  logs:             %s\n", logs)
	fmt.Fprintf(out, "  cache:            %s\n", cache)
	fmt.Fprintf(out, "  mirror_local:     %t\n", p.EffectiveMirrorLocal())

	fmt.Fprintln(out)
	fmt.Fprintln(out, "resolution chain considered:")
	for _, row := range chainRows(chain) {
		if row.Source == string(chain.Source) {
			line := fmt.Sprintf("%s ← selected", row.Name)
			if chain.Source == profile.ChainSourceDetect && chain.DetectVia != "" {
				line = fmt.Sprintf("%s (matched %s=%s) ← selected", row.Name, chain.DetectVia, os.Getenv(chain.DetectVia))
			}
			fmt.Fprintf(out, "  %-12s %s\n", row.Source, line)
			continue
		}
		fmt.Fprintf(out, "  %-12s %s\n", row.Source, row.Reason)
	}
	return nil
}

// effectiveSourceDetail renders the parenthetical on the `source:` line:
// where the winning selection came from.
func effectiveSourceDetail(chain profile.Chain, cfgPath string) string {
	switch chain.Source {
	case profile.ChainSourceFlag:
		return fmt.Sprintf("flag (--profile %s)", chain.Selected)
	case profile.ChainSourceDetect:
		return fmt.Sprintf("detect (matched %s=%s)", chain.DetectVia, os.Getenv(chain.DetectVia))
	case profile.ChainSourceBuiltin:
		return "builtin (synthesized laptop fallback; no profiles.yaml match)"
	default:
		return fmt.Sprintf("%s (%s)", chain.Source, displayConfigPath(cfgPath))
	}
}

// --- shared chain + surface rendering ---

// chainRows reconstructs the five canonical resolution levels in
// precedence order, merging the chain's selected level back with its
// Considered entries. The project row always reads "not yet wired (step
// 9)" (projectHint is unwired here); the builtin row reads "not reached"
// when something higher won; everything else reuses the resolver's
// reason so this command, the run flow, and step 8's run_start banner
// stay a single source of truth.
func chainRows(chain profile.Chain) []profileConsideredJSON {
	order := []profile.ChainSource{
		profile.ChainSourceFlag,
		profile.ChainSourceProject,
		profile.ChainSourceDetect,
		profile.ChainSourceDefault,
		profile.ChainSourceBuiltin,
	}
	bySource := make(map[profile.ChainSource]profile.ConsideredEntry, len(chain.Considered))
	for _, e := range chain.Considered {
		bySource[e.Source] = e
	}
	rows := make([]profileConsideredJSON, 0, len(order))
	for _, src := range order {
		switch {
		case src == chain.Source:
			rows = append(rows, profileConsideredJSON{Source: string(src), Name: chain.Selected, Reason: "selected"})
		case src == profile.ChainSourceProject:
			rows = append(rows, profileConsideredJSON{Source: string(src), Name: "", Reason: "not yet wired (step 9)"})
		case src == profile.ChainSourceBuiltin:
			rows = append(rows, profileConsideredJSON{Source: string(src), Name: "", Reason: "not reached"})
		default:
			e := bySource[src]
			rows = append(rows, profileConsideredJSON{Source: string(src), Name: e.Name, Reason: e.Reason})
		}
	}
	return rows
}

// effectiveSurfaces renders the resolved profile's state/logs/cache as
// the strings shown in both output modes. It mirrors the orchestrator's
// profileSurfaceSpecs precedence (explicit surfaces > controller
// implication > local sqlite fallback) without filling concrete paths,
// so the output reflects what the profile declares.
func effectiveSurfaces(p *profile.Profile) (state, logs, cache string) {
	surf := p.Surfaces()
	if surf.State == nil && surf.Cache == nil && surf.Logs == nil && p.Controller != "" {
		c := "controller://" + p.Name
		return c, c, c
	}
	state = specString(surf.State)
	if surf.State == nil && p.Controller == "" {
		// Bare profile: the run path falls back to local SQLite state
		// with no shared logs/cache.
		state = "sqlite"
	}
	return state, specString(surf.Logs), specString(surf.Cache)
}

// specString stringifies a backend spec for display. It never emits a
// postgres/mysql DSN URL (those carry credentials); only the type and an
// optional url_source indirection.
func specString(s *backends.Spec) string {
	if s == nil {
		return "-"
	}
	switch s.Type {
	case backends.TypeSQLite:
		if s.Path != "" {
			return "sqlite:" + s.Path
		}
		return "sqlite"
	case backends.TypeS3, backends.TypeGCS, backends.TypeAzureBlob:
		out := s.Type + "://" + s.Bucket
		if s.Prefix != "" {
			out += "/" + s.Prefix
		}
		return out
	case backends.TypeFilesystem:
		return "filesystem:" + s.Path
	case backends.TypeController:
		return "controller://" + s.Controller
	case backends.TypePostgres, backends.TypeMySQL:
		if s.URLSource != "" {
			return s.Type + ":" + s.URLSource
		}
		return s.Type
	case backends.TypeStdout:
		return "stdout"
	default:
		return s.Type
	}
}

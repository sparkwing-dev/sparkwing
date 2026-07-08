// `sparkwing docs versions` enumerates what doc versions this CLI
// (embedded) and sparkwing.dev (with --web) know about. Default
// output is hermetic: only the embedded version shows up. --web
// merges in the remote /versions.json contents.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	flag "github.com/spf13/pflag"
	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

// versionRow is the JSON shape of `sparkwing docs versions -o json`.
// IsLatest is local to the CLI surface (the server's versions.json
// flags the latest via a top-level "latest" key; this layer translates
// that into a per-row boolean for agent ergonomics).
type versionRow struct {
	Version  string `json:"version"`
	Source   string `json:"source"` // "embedded" | "remote"
	IsLatest bool   `json:"is_latest"`
	Notes    string `json:"notes,omitempty"`
}

func runDocsVersions(args []string) error {
	fs := flag.NewFlagSet(cmdDocsVersions.Path, flag.ContinueOnError)
	var output string
	var wf docsWebFlags
	fs.StringVarP(&output, "output", "o", "pretty", "pretty | json | plain")
	registerWebFlags(fs, &wf, false)
	if err := parseAndCheck(cmdDocsVersions, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("docs versions: unexpected positional %q", fs.Arg(0))
	}

	embedded := embeddedDocVersions()
	cliVersion := embeddedVersion()

	rows := make([]versionRow, 0, len(embedded))
	seen := map[string]int{}
	for _, v := range embedded {
		row := versionRow{Version: v, Source: "embedded"}
		if v == cliVersion {
			row.Notes = "this CLI"
		}
		seen[v] = len(rows)
		rows = append(rows, row)
	}

	if wf.web {
		ctx, cancel := newWebContext()
		defer cancel()
		client := docs.NewWebClient()
		client.NoCache = wf.noCache
		v, err := client.Versions(ctx)
		if err != nil {
			return fmt.Errorf("docs versions --web: %w", err)
		}
		for _, ver := range v.Versions {
			if i, ok := seen[ver]; ok {
				if rows[i].Source == "embedded" {
					rows[i].Source = "embedded+remote"
				}
				if ver == v.Latest {
					rows[i].IsLatest = true
				}
				continue
			}
			row := versionRow{Version: ver, Source: "remote", IsLatest: ver == v.Latest}
			seen[ver] = len(rows)
			rows = append(rows, row)
		}
	}

	sort.Slice(rows, func(i, j int) bool {
		return semver.Compare(rows[i].Version, rows[j].Version) > 0
	})

	switch strings.ToLower(output) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	case "plain":
		for _, r := range rows {
			fmt.Println(r.Version)
		}
		return nil
	case "pretty", "table", "":
		return renderVersionsTable(rows)
	default:
		return fmt.Errorf("unknown output format %q (valid: pretty, json, plain)", output)
	}
}

func renderVersionsTable(rows []versionRow) error {
	if len(rows) == 0 {
		fmt.Println(color.Dim("(no doc versions known)"))
		return nil
	}
	verW := len("VERSION")
	srcW := len("SOURCE")
	for _, r := range rows {
		if n := len(r.Version); n > verW {
			verW = n
		}
		if n := len(r.Source); n > srcW {
			srcW = n
		}
	}
	fmt.Printf("%s  %s  %s\n",
		color.Bold(fmt.Sprintf("%-*s", verW, "VERSION")),
		color.Bold(fmt.Sprintf("%-*s", srcW, "SOURCE")),
		color.Bold("NOTES"))
	for _, r := range rows {
		notes := r.Notes
		if r.IsLatest {
			if notes != "" {
				notes = "latest, " + notes
			} else {
				notes = "latest"
			}
		}
		fmt.Printf("%-*s  %-*s  %s\n",
			verW, r.Version,
			srcW, r.Source,
			color.Dim(notes))
	}
	return nil
}

// embeddedDocVersions returns the set of versions this binary has
// content for. Today the embed is single-version (the CLI's own
// version), but the embed also includes one row per migration guide
// shipped under content/migrations/, which counts as "this CLI knows
// the markdown for that release."
func embeddedDocVersions() []string {
	set := map[string]struct{}{}
	if v := embeddedVersion(); v != "" {
		set[v] = struct{}{}
	}
	for _, e := range docs.MigrationsList() {
		if semver.IsValid(e.Version) {
			set[e.Version] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		return semver.Compare(out[i], out[j]) > 0
	})
	return out
}

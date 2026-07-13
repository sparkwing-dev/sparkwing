// `sparkwing version` is the kubectl-style version surface: one
// command shows the CLI version, the latest published release
// (network-checked, ~3s timeout), the SDK pin in the current
// repo's .sparkwing/go.mod, and any sparks libraries declared
// alongside it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	flag "github.com/spf13/pflag"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/docs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// VersionReport is the JSON shape of `sparkwing version --json`.
type VersionReport struct {
	CLI            InfoVersion     `json:"cli"`
	SchemaVersion  int             `json:"schema_version"`
	LatestRelease  string          `json:"latest_release,omitempty"`
	LatestFetchErr string          `json:"latest_fetch_error,omitempty"`
	Behind         bool            `json:"behind"`
	Hold           *versionHold    `json:"hold,omitempty"`
	Project        *VersionProject `json:"project,omitempty"`
}

// VersionProject collects per-repo version info: the SDK pin in
// .sparkwing/go.mod and any sparks library pins. Nil when the
// command is run outside a sparkwing project.
type VersionProject struct {
	SparkwingDir string     `json:"sparkwing_dir"`
	SDKPin       string     `json:"sdk_pin,omitempty"`
	SDKReplace   string     `json:"sdk_replace,omitempty"`
	SDKBehind    bool       `json:"sdk_behind"`
	Sparks       []SparkPin `json:"sparks,omitempty"`
}

// SparkPin is one sparkwing-dev/sparks-* module declared in the
// project's go.mod (and presumably its sparks.yaml).
type SparkPin struct {
	Module  string `json:"module"`
	Version string `json:"version"`
}

// versionLatestURL is the canonical "what's the newest tag"
// pointer. github.com/.../releases/latest 302-redirects to
// /releases/tag/<latest-tag>; we HEAD it and parse the tag from
// the Location header. No GitHub API token, no rate-limited JSON.
const versionLatestURL = "https://github.com/sparkwing-dev/sparkwing/releases/latest"

// versionFetchTimeout caps the latest-release lookup. Short enough
// that an offline laptop still gets a useful command in seconds,
// long enough that a slow link succeeds.
const versionFetchTimeout = 3 * time.Second

// sdkModulePath is the canonical Go module path for the sparkwing
// SDK. Used both for the SDK-pin lookup and for distinguishing the
// SDK from sparks-* sibling modules.
const sdkModulePath = "github.com/sparkwing-dev/sparkwing"

func runVersion(args []string) error {
	if len(args) > 0 && args[0] == "update" {
		return runVersionUpdate(args[1:])
	}
	if len(args) > 0 && args[0] == "hold" {
		return runVersionHold(args[1:])
	}
	fs := flag.NewFlagSet(cmdVersion.Path, flag.ContinueOnError)
	var output string
	fs.StringVarP(&output, "output", "o", "pretty", "pretty | json | plain")
	offline := fs.Bool("offline", false, "skip the network fetch for latest release")
	changelog := fs.Bool("changelog", false, "print the changelog for the installed release")
	if err := parseAndCheck(cmdVersion, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("version: unexpected positional %q", fs.Arg(0))
	}

	report := gatherVersionReport(*offline)

	if *changelog {
		printVersionChangelog(os.Stdout, report)
		return nil
	}

	if output == "table" {
		output = "pretty"
	}
	switch strings.ToLower(output) {
	case "json", "":
		if output == "" {
			output = "pretty"
		}
		if output == "json" {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(report)
		}
	case "plain":
		fmt.Println(report.CLI.Installed)
		if report.LatestRelease != "" {
			fmt.Println(report.LatestRelease)
		}
		return nil
	}
	printVersionTable(report)
	return nil
}

func gatherVersionReport(offline bool) VersionReport {
	r := VersionReport{
		CLI:           parseInfoVersion(installedVersion()),
		SchemaVersion: store.ExpectedSchemaVersion(),
	}
	if !offline {
		latest, err := fetchLatestRelease()
		if err != nil {
			r.LatestFetchErr = err.Error()
		} else {
			r.LatestRelease = latest
		}
	}
	if r.CLI.Semver != "" && r.LatestRelease != "" {
		r.Behind = semver.Compare(r.CLI.Semver, r.LatestRelease) < 0
	}
	if hold := resolveVersionHold(); hold.Value != "" {
		r.Hold = &hold
	}
	if proj := gatherVersionProject(r.LatestRelease); proj != nil {
		r.Project = proj
	}
	return r
}

// gatherVersionProject walks up from cwd to find .sparkwing/, then
// parses .sparkwing/go.mod for the SDK pin and any sparks-* pins.
// Returns nil when no .sparkwing/ is found so the report's Project
// field stays absent in JSON.
func gatherVersionProject(latest string) *VersionProject {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	sparkwingDir, ok := walkUpForSparkwing(cwd)
	if !ok {
		return nil
	}
	gomodPath := filepath.Join(sparkwingDir, "go.mod")
	body, err := os.ReadFile(gomodPath)
	if err != nil {
		return &VersionProject{SparkwingDir: sparkwingDir}
	}
	mf, err := modfile.Parse(gomodPath, body, nil)
	if err != nil {
		return &VersionProject{SparkwingDir: sparkwingDir}
	}

	proj := &VersionProject{SparkwingDir: sparkwingDir}
	for _, req := range mf.Require {
		mod := req.Mod.Path
		ver := req.Mod.Version
		switch {
		case mod == sdkModulePath:
			proj.SDKPin = ver
		case strings.HasPrefix(mod, "github.com/sparkwing-dev/sparks-"):
			proj.Sparks = append(proj.Sparks, SparkPin{Module: mod, Version: ver})
		}
	}
	for _, rep := range mf.Replace {
		if rep.Old.Path != sdkModulePath {
			continue
		}
		target := rep.New.Path
		if rep.New.Version != "" {
			target = target + "@" + rep.New.Version
		}
		proj.SDKReplace = target
		break
	}
	if proj.SDKReplace == "" && proj.SDKPin != "" && latest != "" && isSemver(proj.SDKPin) {
		proj.SDKBehind = semver.Compare(proj.SDKPin, latest) < 0
	}
	sort.Slice(proj.Sparks, func(i, j int) bool { return proj.Sparks[i].Module < proj.Sparks[j].Module })
	return proj
}

// fetchLatestRelease HEADs github.com/.../releases/latest -- which
// 302-redirects to /releases/tag/<latest-tag> -- and parses the tag
// out of the Location header. Bounded by versionFetchTimeout so an
// offline laptop falls back to "(unknown)" quickly.
func fetchLatestRelease() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), versionFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, versionLatestURL, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("%s: no Location header (status %d)", versionLatestURL, resp.StatusCode)
	}
	const marker = "/releases/tag/"
	idx := strings.LastIndex(loc, marker)
	if idx < 0 {
		return "", fmt.Errorf("unexpected redirect target %q", loc)
	}
	v := strings.TrimSpace(loc[idx+len(marker):])
	if !isSemver(v) {
		return "", fmt.Errorf("malformed tag %q from %s", v, loc)
	}
	return v, nil
}

// isSemver returns true for vX.Y.Z (allowing pre-release/build
// suffixes per golang.org/x/mod/semver). Used to gate compare
// calls so a malformed string doesn't poison the "behind" boolean.
func isSemver(v string) bool {
	return semver.IsValid(v)
}

// printVersionChangelog renders the embedded changelog for the
// installed release. The embedded corpus only covers up to the running
// binary's own version, so this is best-effort offline: it prints the
// installed version's section (or [Unreleased] for a dev build), and
// when the network check knows a newer release exists it points at the
// release page rather than pretending to have notes it cannot embed.
func printVersionChangelog(w io.Writer, r VersionReport) {
	const releasesURL = "https://github.com/sparkwing-dev/sparkwing/releases"
	printed := false
	if r.CLI.Semver != "" {
		if body, ok := docs.ChangelogSection(r.CLI.Semver); ok {
			fmt.Fprintf(w, "%s\n\n%s\n", color.Bold("sparkwing "+r.CLI.Semver), body)
			printed = true
		}
	}
	if !printed {
		if body, ok := docs.ChangelogSection("Unreleased"); ok && strings.TrimSpace(body) != "" {
			fmt.Fprintf(w, "%s\n\n%s\n", color.Bold("sparkwing (unreleased)"), body)
			printed = true
		}
	}
	if !printed {
		fmt.Fprintf(w, "no embedded changelog entry for %s\n", r.CLI.Installed)
		fmt.Fprintf(w, "full changelog: %s -- see %s\n",
			"sparkwing docs read --topic "+docs.ChangelogSlug, releasesURL)
		return
	}
	fmt.Fprintln(w)
	if r.Behind {
		fmt.Fprintf(w, "%s newer releases exist -- see %s\n", color.Yellow("note:"), releasesURL)
	}
	fmt.Fprintf(w, "full changelog: %s\n", "sparkwing docs read --topic "+docs.ChangelogSlug)
}

func printVersionTable(r VersionReport) {
	cv := r.CLI
	fmt.Printf("CLI\n")
	fmt.Printf("  version:  %s\n", cv.Installed)
	fmt.Printf("  build:    %s", cv.BuildType)
	if cv.HumanLabel != "" {
		fmt.Printf(" %s", color.Dim("("+cv.HumanLabel+")"))
	}
	fmt.Println()
	switch {
	case r.LatestFetchErr != "":
		fmt.Printf("  latest:   %s %s\n", color.Dim("unknown"), color.Dim("("+r.LatestFetchErr+")"))
	case r.LatestRelease == "":
		fmt.Printf("  latest:   %s\n", color.Dim("not checked (--offline)"))
	case r.Behind:
		fmt.Printf(
			"  latest:   %s %s\n",
			r.LatestRelease,
			color.Yellow("behind"),
		)
	default:
		fmt.Printf("  latest:   %s %s\n", r.LatestRelease, color.Green("up to date"))
	}
	if r.Behind {
		fmt.Printf("  upgrade:  %s\n", "sparkwing version update --cli")
	} else {
		fmt.Printf("  upgrade:  %s\n", color.Dim("sparkwing version update --cli"))
	}
	if r.Hold != nil {
		fmt.Printf("  hold:     %s %s\n",
			color.Yellow("held at "+r.Hold.Value+" by operator"),
			color.Dim("("+r.Hold.Source+")"))
	}
	fmt.Printf("  releases: https://github.com/sparkwing-dev/sparkwing/releases\n")
	fmt.Printf("  schema:   %d %s\n", r.SchemaVersion, color.Dim("(embedded runs-store schema)"))
	fmt.Println()

	if r.Project == nil {
		fmt.Println(color.Dim("(no .sparkwing/ in this directory or any parent)"))
		return
	}

	p := r.Project
	if p.SDKPin == "" && p.SDKReplace == "" && len(p.Sparks) == 0 {
		fmt.Printf("Project at %s\n", p.SparkwingDir)
		fmt.Println(color.Dim("  (no go.mod pins resolved)"))
		return
	}
	fmt.Printf("Project at %s\n", p.SparkwingDir)
	switch {
	case p.SDKReplace != "":
		fmt.Printf("  sdk:      %s\n", color.Dim("(replaced with "+p.SDKReplace+")"))
	case p.SDKPin != "":
		label := color.Green("up to date")
		switch {
		case r.LatestRelease == "":
			label = color.Dim("(latest unknown)")
		case p.SDKBehind:
			label = color.Yellow("behind") + " " + color.Dim("(latest "+r.LatestRelease+")")
		}
		fmt.Printf("  sdk:      %s   %s\n", p.SDKPin, label)
		if p.SDKBehind {
			fmt.Printf("  upgrade:  sparkwing version update --sdk\n")
		}
	}
	if len(p.Sparks) > 0 {
		fmt.Println("  sparks:")
		w := 0
		for _, s := range p.Sparks {
			if n := len(s.Module); n > w {
				w = n
			}
		}
		for _, s := range p.Sparks {
			fmt.Printf("    %-*s  %s\n", w, s.Module, s.Version)
		}
	}
}

// `sparkwing agents` -- terminal view of the fleet. The dashboard at
// /agents pulls the same data via /api/v1/agents; this subcommand
// reuses that endpoint so operators can script fleet checks or debug
// the dashboard without a browser.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	flag "github.com/spf13/pflag"
)

func runAgents(args []string) error {
	if handleParentHelp(cmdAgents, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdAgents, os.Stderr)
		return errors.New("agents: subcommand required (list)")
	}
	switch args[0] {
	case "list":
		return runAgentsList(args[1:])
	default:
		PrintHelp(cmdAgents, os.Stderr)
		return fmt.Errorf("agents: unknown subcommand %q", args[0])
	}
}

// agentView is the JSON shape the controller returns from
// /api/v1/agents. Kept local rather than importing pkg/controller so
// we don't drag the controller's storage deps into the CLI binary.
// The shape is already stable across the dashboard + this command.
type agentView struct {
	Name          string            `json:"name"`
	Type          string            `json:"type"`
	Labels        map[string]string `json:"labels"`
	LastSeen      string            `json:"last_seen"`
	Status        string            `json:"status"`
	ActiveJobs    []string          `json:"active_jobs"`
	MaxConcurrent int               `json:"max_concurrent"`
}

type agentsResp struct {
	Agents []agentView `json:"agents"`
}

func runAgentsList(args []string) error {
	fs := flag.NewFlagSet(cmdAgentsList.Path, flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON instead of a table")
	outputFormat := fs.StringP("output", "o", "", "output format (json|table)")
	quiet := fs.BoolP("quiet", "q", false, "print just agent names, one per line")
	on := addProfileFlag(fs)
	if err := parseAndCheck(cmdAgentsList, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if *on == "" {
		return errors.New("agents list: --on is required")
	}
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "agents list"); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	agents, err := fetchAgents(ctx, prof.Controller, prof.Token)
	if err != nil {
		return fmt.Errorf("agents list: %w", err)
	}

	// Stable order by name so repeated invocations diff cleanly.
	sort.Slice(agents, func(i, j int) bool {
		if agents[i].Type != agents[j].Type {
			return agents[i].Type < agents[j].Type
		}
		return agents[i].Name < agents[j].Name
	})

	if *quiet {
		for _, a := range agents {
			fmt.Fprintln(os.Stdout, a.Name)
		}
		return nil
	}

	if *asJSON || *outputFormat == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(agents)
	}

	if len(agents) == 0 {
		fmt.Fprintln(os.Stdout, "(no agents in the last hour)")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTYPE\tSTATUS\tACTIVE\tLAST_SEEN\tLABELS")
	for _, a := range agents {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\n",
			a.Name, a.Type, a.Status, len(a.ActiveJobs),
			formatAgentLastSeen(a.LastSeen),
			formatLabels(a.Labels))
	}
	return tw.Flush()
}

// fetchAgents calls GET /api/v1/agents on the given controller with
// bearer auth. The controller's client pkg doesn't have a typed
// wrapper for this endpoint yet; we go direct to avoid widening the
// public client surface for one dashboard-facing GET.
func fetchAgents(ctx context.Context, baseURL, token string) ([]agentView, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/v1/agents", nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET /api/v1/agents: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var out agentsResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode agents response: %w", err)
	}
	return out.Agents, nil
}

func formatAgentLastSeen(rfc string) string {
	if rfc == "" {
		return "-"
	}
	ts, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		return rfc
	}
	return ts.UTC().Format("2006-01-02 15:04")
}

func formatLabels(m map[string]string) string {
	if len(m) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ",")
}

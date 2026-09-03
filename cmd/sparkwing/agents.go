package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/ndjson"

	flag "github.com/spf13/pflag"
)

func runAgents(args []string) error {
	if handleParentHelp(cmdAgents, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdAgents, os.Stderr)
		return errors.New("agents: subcommand required (list|enroll)")
	}
	switch args[0] {
	case "list":
		return runAgentsList(args[1:])
	case "enroll":
		return runAgentsEnroll(args[1:])
	default:
		PrintHelp(cmdAgents, os.Stderr)
		return fmt.Errorf("agents: unknown subcommand %q", args[0])
	}
}

type agentView struct {
	Name            string            `json:"name"`
	Type            string            `json:"type"`
	Location        string            `json:"location"`
	Labels          map[string]string `json:"labels"`
	Capabilities    []string          `json:"capabilities,omitempty"`
	LastSeen        string            `json:"last_seen"`
	Status          string            `json:"status"`
	ActiveJobs      []string          `json:"active_jobs"`
	ActiveSlots     *int              `json:"active_slots,omitempty"`
	MaxConcurrent   int               `json:"max_concurrent"`
	BasePriority    int               `json:"base_priority"`
	PriorityCeiling int               `json:"priority_ceiling"`
	Budget          agentResources    `json:"budget"`
	Headroom        *agentHeadroom    `json:"headroom,omitempty"`
}

type agentResources struct {
	Cores       float64 `json:"cores"`
	MemoryBytes int64   `json:"memory_bytes"`
}

type agentHeadroom struct {
	agentResources
	QueueDepth int `json:"queue_depth"`
}

type agentsResp struct {
	Agents []agentView `json:"agents"`
}

type agentEnrollment struct {
	TokenPrefix     string         `json:"token_prefix"`
	Kind            string         `json:"kind"`
	Location        string         `json:"location"`
	Capabilities    []string       `json:"capabilities,omitempty"`
	BasePriority    int            `json:"base_priority"`
	PriorityCeiling int            `json:"priority_ceiling"`
	MaxConcurrent   int            `json:"max_concurrent"`
	Budget          agentResources `json:"budget"`
}

func runAgentsEnroll(args []string) error {
	fs := flag.NewFlagSet(cmdAgentsEnroll.Path, flag.ContinueOnError)
	name := fs.String("name", "", "executor name")
	tokenPrefix := fs.String("token-prefix", "", "exact runner or service token prefix")
	kind := fs.String("kind", "agent", "executor kind (agent|gateway)")
	location := fs.String("location", "unknown", "trusted placement location (local|cloud|unknown)")
	capabilities := fs.StringSlice("capability", nil, "trusted capability (repeatable)")
	basePriority := fs.Int("base-priority", 0, "base scheduling priority (0-100)")
	priorityCeiling := fs.Int("priority-ceiling", 100, "highest effective priority (0-100)")
	maxConcurrent := fs.Int("max-concurrent", 1, "trusted concurrent slot ceiling")
	budgetCores := fs.Float64("budget-cores", 0, "trusted CPU contribution ceiling (0 = uncapped)")
	budgetMemoryBytes := fs.Int64("budget-memory-bytes", 0, "trusted memory contribution ceiling in bytes (0 = uncapped)")
	on := addProfileFlag(fs)
	if err := parseAndCheck(cmdAgentsEnroll, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if *on == "" {
		return errors.New("agents enroll: --profile is required")
	}
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "agents enroll"); err != nil {
		return err
	}
	body := agentEnrollment{
		TokenPrefix: *tokenPrefix, Kind: *kind, Location: *location,
		Capabilities: *capabilities, BasePriority: *basePriority,
		PriorityCeiling: *priorityCeiling, MaxConcurrent: *maxConcurrent,
		Budget: agentResources{Cores: *budgetCores, MemoryBytes: *budgetMemoryBytes},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := enrollAgent(ctx, prof.ControllerURL(), prof.ControllerToken(), *name, body); err != nil {
		return fmt.Errorf("agents enroll: %w", err)
	}
	fmt.Fprintf(os.Stdout, "enrolled %s\n", *name)
	return nil
}

func enrollAgent(ctx context.Context, baseURL, token, name string, enrollment agentEnrollment) error {
	buf, err := json.Marshal(enrollment)
	if err != nil {
		return err
	}
	path := strings.TrimRight(baseURL, "/") + "/api/v1/agents/" + neturl.PathEscape(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, path, strings.NewReader(string(buf)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("PUT /api/v1/agents/{name}: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func runAgentsList(args []string) error {
	fs := flag.NewFlagSet(cmdAgentsList.Path, flag.ContinueOnError)
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
		return errors.New("agents list: --profile is required")
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
	agents, err := fetchAgents(ctx, prof.ControllerURL(), prof.ControllerToken())
	if err != nil {
		return fmt.Errorf("agents list: %w", err)
	}

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

	if *outputFormat == "json" {
		return ndjson.Write(os.Stdout, agents)
	}

	if len(agents) == 0 {
		fmt.Fprintln(os.Stdout, "(no registered executors or recent legacy agents)")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTYPE\tLOCATION\tSTATUS\tACTIVE\tHEADROOM\tLAST_SEEN\tCAPABILITIES")
	for _, a := range agents {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d/%s\t%s\t%s\t%s\n",
			a.Name, a.Type, a.Location, a.Status, agentActiveSlots(a), formatAgentLimit(a.MaxConcurrent),
			formatAgentHeadroom(a.Headroom),
			formatAgentLastSeen(a.LastSeen),
			formatCapabilities(a.Capabilities, a.Labels))
	}
	return tw.Flush()
}

func agentActiveSlots(agent agentView) int {
	if agent.ActiveSlots != nil {
		return *agent.ActiveSlots
	}
	return len(agent.ActiveJobs)
}

func formatAgentLimit(limit int) string {
	if limit <= 0 {
		return "-"
	}
	return fmt.Sprint(limit)
}

func formatAgentHeadroom(h *agentHeadroom) string {
	if h == nil {
		return "-"
	}
	return fmt.Sprintf("%.1fc/%s", h.Cores, formatAgentMemory(h.MemoryBytes))
}

func formatAgentMemory(bytes int64) string {
	if bytes <= 0 {
		return "0GiB"
	}
	return fmt.Sprintf("%.1fGiB", float64(bytes)/float64(1<<30))
}

func formatCapabilities(capabilities []string, labels map[string]string) string {
	if len(capabilities) > 0 {
		return strings.Join(capabilities, ",")
	}
	return formatLabels(labels)
}

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

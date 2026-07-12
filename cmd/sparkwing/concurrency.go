// `sparkwing cluster concurrency` -- inspect a concurrency namespace's
// holders and queue. The dashboard reads the same /api/v1/concurrency/
// {key}/state endpoint; this subcommand reuses it so an operator can
// tell wedged work from work waiting for budget without a browser.
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
	"strings"
	"text/tabwriter"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
)

// concState mirrors the controller's GET /api/v1/concurrency/{key}/state
// response. Kept local so the CLI binary doesn't import the controller's
// storage deps (same convention as agentView).
type concState struct {
	Key               string       `json:"key"`
	Capacity          int          `json:"capacity"`
	EffectiveCapacity int          `json:"effective_capacity"`
	UsedCost          int          `json:"used_cost"`
	Holders           []concHolder `json:"holders"`
	Waiters           []concWaiter `json:"waiters"`
}

type concHolder struct {
	HolderID       string    `json:"holder_id"`
	RunID          string    `json:"run_id"`
	NodeID         string    `json:"node_id"`
	ClaimedAt      time.Time `json:"claimed_at"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
	Superseded     bool      `json:"superseded"`
	Cost           int       `json:"cost"`
}

type concWaiter struct {
	RunID     string    `json:"run_id"`
	NodeID    string    `json:"node_id"`
	ArrivedAt time.Time `json:"arrived_at"`
	Policy    string    `json:"policy"`
	Cost      int       `json:"cost"`
	Position  int       `json:"position"`
}

func runConcurrency(args []string) error {
	fs := flag.NewFlagSet(cmdClusterConcurrency.Path, flag.ContinueOnError)
	namespace := fs.String("namespace", "", "concurrency namespace to inspect")
	outputFormat := fs.StringP("output", "o", "", "output format (json|table)")
	on := addProfileFlag(fs)
	if err := parseAndCheck(cmdClusterConcurrency, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if *namespace == "" {
		return errors.New("cluster concurrency: --namespace is required")
	}
	if *on == "" {
		return errors.New("cluster concurrency: --profile is required")
	}
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "cluster concurrency"); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := fetchConcurrencyState(ctx, prof.ControllerURL(), prof.ControllerToken(), *namespace)
	if err != nil {
		return fmt.Errorf("cluster concurrency: %w", err)
	}

	if *outputFormat == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(st)
	}
	renderConcurrencyState(os.Stdout, st)
	return nil
}

// fetchConcurrencyState calls GET /api/v1/concurrency/{key}/state with
// bearer auth. Goes direct rather than through the typed client to avoid
// widening the public client surface for one dashboard-facing GET (same
// rationale as fetchAgents).
func fetchConcurrencyState(ctx context.Context, baseURL, token, namespace string) (*concState, error) {
	u := strings.TrimRight(baseURL, "/") + "/api/v1/concurrency/" + neturl.PathEscape(namespace) + "/state"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
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
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("namespace %q not found (never declared, or no active holders or waiters)", namespace)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET concurrency state: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var out concState
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode concurrency state: %w", err)
	}
	return &out, nil
}

func renderConcurrencyState(w io.Writer, st *concState) {
	active := 0
	for _, h := range st.Holders {
		if !h.Superseded {
			active++
		}
	}
	eff := st.EffectiveCapacity
	if eff == 0 {
		eff = st.Capacity
	}
	available := eff - st.UsedCost
	if available < 0 {
		available = 0
	}
	fmt.Fprintf(w, "namespace: %s   capacity: %d   effective: %d   budget used: %d   available: %d   held: %d   queued: %d\n",
		st.Key, st.Capacity, eff, st.UsedCost, available, active, len(st.Waiters))
	fmt.Fprintf(w, "scope: %s\n\n", scopeFromKey(st.Key))

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "HOLDER\tRUN/NODE\tCOST\tCLAIMED\tLEASE_EXPIRES")
	if len(st.Holders) == 0 {
		fmt.Fprintln(tw, "(none)\t\t\t\t")
	}
	for _, h := range st.Holders {
		marker := ""
		if h.Superseded {
			marker = " (superseded)"
		}
		fmt.Fprintf(tw, "%s%s\t%s\t%d\t%s\t%s\n", h.HolderID, marker, runNode(h.RunID, h.NodeID),
			h.Cost, fmtConcTime(h.ClaimedAt), fmtConcTime(h.LeaseExpiresAt))
	}
	_ = tw.Flush()

	fmt.Fprintln(w)
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "POS\tRUN/NODE\tPOLICY\tCOST\tARRIVED")
	if len(st.Waiters) == 0 {
		fmt.Fprintln(tw, "-\t(no one queued)\t\t\t")
	}
	for _, q := range st.Waiters {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%s\n", q.Position, runNode(q.RunID, q.NodeID), q.Policy, q.Cost, fmtConcTime(q.ArrivedAt))
	}
	_ = tw.Flush()
}

// scopeFromKey reports the scope a concurrency key encodes. The key
// scheme (scope-tag prefix) is owned by the orchestrator; this defers
// to its parser so the CLI label can't drift from the producer.
func scopeFromKey(key string) string {
	return orchestrator.ScopeLabelFromKey(key)
}

func runNode(runID, nodeID string) string {
	if nodeID == "" {
		return runID
	}
	return runID + "/" + nodeID
}

func fmtConcTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04:05")
}

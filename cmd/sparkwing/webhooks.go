// `sparkwing webhooks` -- sparkwing-aware wrapper over the GitHub
// hooks API. Cross-references GitHub delivery metadata with
// sparkwing trigger/run state so operators can debug a webhook
// cycle without bouncing between gh and the sparkwing dashboard.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	neturl "net/url"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/controller/client"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

func runWebhooks(args []string) error {
	if handleParentHelp(cmdWebhooks, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdWebhooks, os.Stderr)
		return errors.New("webhooks: subcommand required (list|deliveries|replay)")
	}
	switch args[0] {
	case "list":
		return runWebhooksList(args[1:])
	case "deliveries":
		return runWebhooksDeliveries(args[1:])
	case "replay":
		return runWebhooksReplay(args[1:])
	default:
		PrintHelp(cmdWebhooks, os.Stderr)
		return fmt.Errorf("webhooks: unknown subcommand %q", args[0])
	}
}

// ghCLIAvailable verifies that the gh CLI is reachable. Called up
// front by every webhooks subcommand so we fail with a clear install
// hint instead of a cryptic exec error midway through.
func ghCLIAvailable() error {
	if _, err := exec.LookPath("gh"); err != nil {
		return errors.New("gh CLI not found on PATH. install: https://cli.github.com")
	}
	return nil
}

// normalizeRepo accepts "OWNER/NAME" or bare "NAME" (falling back
// to gh's configured default owner). An empty return value means
// "couldn't resolve" and callers surface that as an error.
func normalizeRepo(repo string) (string, error) {
	if strings.Contains(repo, "/") {
		return repo, nil
	}
	out, err := exec.Command("gh", "api", "user", "--jq", ".login").Output()
	if err != nil {
		return "", fmt.Errorf("--repo %q has no owner prefix and `gh api user` failed: %w", repo, err)
	}
	owner := strings.TrimSpace(string(out))
	if owner == "" {
		return "", fmt.Errorf("--repo %q has no owner prefix and gh returned no default owner", repo)
	}
	return owner + "/" + repo, nil
}

// ghAPI runs `gh api <path>` and JSON-decodes stdout into out.
// Propagates stderr on failure so gh's own error text surfaces
// (rate-limit, 404, auth prompts) without wrapping.
func ghAPI(path string, out any) error {
	cmd := exec.Command("gh", "api", path)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("gh api %s: %w", path, err)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(stdout, out)
}

// ghAPIPost runs `gh api -X POST <path>` and JSON-decodes stdout
// into out (nil accepted). Factored out of ghAPI so callers that
// POST (replay attempts, eventually delete/create) don't have to
// hand-craft exec.Cmd.
func ghAPIPost(path string, out any) error {
	cmd := exec.Command("gh", "api", "-X", "POST", path)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("gh api POST %s: %w", path, err)
	}
	if out == nil || len(stdout) == 0 {
		return nil
	}
	return json.Unmarshal(stdout, out)
}

// githubHook mirrors the subset of the GitHub hooks API response we
// render. Extra fields are ignored.
type githubHook struct {
	ID           int64              `json:"id"`
	Active       bool               `json:"active"`
	Config       githubHookConfig   `json:"config"`
	LastResponse githubHookLastResp `json:"last_response"`
	Events       []string           `json:"events"`
	UpdatedAt    string             `json:"updated_at"`
	CreatedAt    string             `json:"created_at"`
}

type githubHookConfig struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
}

type githubHookLastResp struct {
	Code    int    `json:"code"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type webhookListRow struct {
	ID         int64  `json:"id"`
	Pipeline   string `json:"pipeline"`
	Active     bool   `json:"active"`
	LastStatus int    `json:"last_status"`
	URL        string `json:"url"`
}

func runWebhooksList(args []string) error {
	fs := flag.NewFlagSet(cmdWebhooksList.Path, flag.ContinueOnError)
	repoFlag := fs.String("repo", "", "GitHub repo (OWNER/NAME)")
	asJSON := fs.BoolP("json", "", false, "emit JSON instead of a table")
	outputFormat := fs.StringP("output", "o", "", "output format (json|table). Matches kubectl/gh")
	on := addProfileFlag(fs)
	_ = on // --on is accepted for symmetry with other verbs; list doesn't need controller state.
	if err := parseAndCheck(cmdWebhooksList, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if *repoFlag == "" {
		return errors.New("webhooks list: --repo is required")
	}
	if err := ghCLIAvailable(); err != nil {
		return fmt.Errorf("webhooks list: %w", err)
	}
	repo, err := normalizeRepo(*repoFlag)
	if err != nil {
		return fmt.Errorf("webhooks list: %w", err)
	}
	var hooks []githubHook
	if err := ghAPI("/repos/"+repo+"/hooks", &hooks); err != nil {
		return fmt.Errorf("webhooks list: %w", err)
	}

	rows := make([]webhookListRow, 0, len(hooks))
	for _, h := range hooks {
		rows = append(rows, webhookListRow{
			ID:         h.ID,
			Pipeline:   derivePipelineFromHookURL(h.Config.URL),
			Active:     h.Active,
			LastStatus: h.LastResponse.Code,
			URL:        h.Config.URL,
		})
	}

	if *asJSON || *outputFormat == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tPIPELINE\tACTIVE\tLAST_STATUS\tURL")
	for _, r := range rows {
		status := ""
		if r.LastStatus != 0 {
			status = fmt.Sprintf("%d", r.LastStatus)
		}
		fmt.Fprintf(tw, "%d\t%s\t%t\t%s\t%s\n", r.ID, r.Pipeline, r.Active, status, r.URL)
	}
	return tw.Flush()
}

// derivePipelineFromHookURL extracts the pipeline name from a
// sparkwing webhook URL. The controller routes POST
// /webhooks/github/<pipeline>; unscoped legacy hooks (pre-#8) posted
// to /webhooks/github with no path segment, so they render as
// "(unscoped, legacy)" to flag them for cleanup.
func derivePipelineFromHookURL(rawURL string) string {
	u, err := neturl.Parse(rawURL)
	if err != nil {
		return "(unknown)"
	}
	path := strings.TrimRight(u.Path, "/")
	prefix := "/webhooks/github"
	if !strings.HasSuffix(path, prefix) && !strings.Contains(path, prefix+"/") {
		return "(non-sparkwing)"
	}
	if strings.HasSuffix(path, prefix) {
		return "(unscoped, legacy)"
	}
	idx := strings.Index(path, prefix+"/")
	if idx < 0 {
		return "(unknown)"
	}
	return path[idx+len(prefix)+1:]
}

type githubDelivery struct {
	ID             int64  `json:"id"`
	GUID           string `json:"guid"`
	DeliveredAt    string `json:"delivered_at"`
	Redelivery     bool   `json:"redelivery"`
	Duration       any    `json:"duration"` // float in seconds; preserved as any to skip precision churn
	Status         string `json:"status"`
	StatusCode     int    `json:"status_code"`
	Event          string `json:"event"`
	Pipeline       string `json:"pipeline"`
	InstallationID int64  `json:"installation_id"`
}

type webhookDeliveryRow struct {
	Delivery   string `json:"delivery"`
	At         string `json:"at"`
	Status     int    `json:"status"`
	TriggerID  string `json:"trigger_id,omitempty"`
	RunStatus  string `json:"run_status,omitempty"`
	Event      string `json:"event,omitempty"`
	Redelivery bool   `json:"redelivery,omitempty"`
}

func runWebhooksDeliveries(args []string) error {
	fs := flag.NewFlagSet(cmdWebhooksDeliveries.Path, flag.ContinueOnError)
	repoFlag := fs.String("repo", "", "GitHub repo (OWNER/NAME)")
	hook := fs.Int64("hook", 0, "GitHub hook id (from 'webhooks list')")
	since := fs.Duration("since", 24*time.Hour, "only deliveries newer than this")
	asJSON := fs.BoolP("json", "", false, "emit JSON instead of a table")
	outputFormat := fs.StringP("output", "o", "", "output format (json|table)")
	on := addProfileFlag(fs)
	if err := parseAndCheck(cmdWebhooksDeliveries, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if *repoFlag == "" {
		return errors.New("webhooks deliveries: --repo is required")
	}
	if *hook == 0 {
		return errors.New("webhooks deliveries: --hook is required")
	}
	if *on == "" {
		return errors.New("webhooks deliveries: --on is required (needed to cross-reference trigger state)")
	}
	if err := ghCLIAvailable(); err != nil {
		return fmt.Errorf("webhooks deliveries: %w", err)
	}
	repo, err := normalizeRepo(*repoFlag)
	if err != nil {
		return fmt.Errorf("webhooks deliveries: %w", err)
	}

	var deliveries []githubDelivery
	// `gh api` auto-paginates when --paginate is passed, but the hook
	// delivery log is only useful for recent entries -- cap at one
	// page (30 rows) by default and filter client-side by --since.
	if err := ghAPI(fmt.Sprintf("/repos/%s/hooks/%d/deliveries?per_page=100", repo, *hook), &deliveries); err != nil {
		return fmt.Errorf("webhooks deliveries: %w", err)
	}

	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "webhooks deliveries"); err != nil {
		return err
	}
	c := client.NewWithToken(prof.Controller, nil, prof.Token)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Build a delivery->run index by walking recent runs in the
	// --since window and pulling each one's trigger row (which carries
	// GITHUB_DELIVERY in TriggerEnv). We cap the scan at a reasonable
	// batch so a stale deliveries feed doesn't fan out into hundreds
	// of trigger lookups; entries that fall outside the window simply
	// render without a trigger_id column.
	recentRuns, err := c.ListRuns(ctx, store.RunFilter{Limit: 200, Since: time.Now().Add(-*since)})
	if err != nil {
		return fmt.Errorf("webhooks deliveries: list runs: %w", err)
	}
	deliveryIndex := buildDeliveryIndex(ctx, c, recentRuns)

	cutoff := time.Now().Add(-*since)
	rows := make([]webhookDeliveryRow, 0, len(deliveries))
	for _, d := range deliveries {
		ts, err := time.Parse(time.RFC3339, d.DeliveredAt)
		if err == nil && ts.Before(cutoff) {
			continue
		}
		row := webhookDeliveryRow{
			Delivery:   d.GUID,
			At:         formatDeliveryTime(d.DeliveredAt),
			Status:     d.StatusCode,
			Event:      d.Event,
			Redelivery: d.Redelivery,
		}
		if hit, ok := deliveryIndex[d.GUID]; ok {
			row.TriggerID = hit.runID
			row.RunStatus = hit.status
		}
		rows = append(rows, row)
	}

	if *asJSON || *outputFormat == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "DELIVERY\tAT\tSTATUS\tEVENT\tTRIGGER_ID\tRUN_STATUS")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\n",
			r.Delivery, r.At, r.Status, r.Event, orDash(r.TriggerID), orDash(r.RunStatus))
	}
	return tw.Flush()
}

// deliveryHit carries the run id + status for one delivery match.
type deliveryHit struct {
	runID  string
	status string
}

// buildDeliveryIndex walks the given runs, fetches each run's trigger
// row (triggers store GITHUB_DELIVERY in their env), and returns a
// map keyed by delivery UUID. Runs with no trigger row or no delivery
// env simply don't land in the map -- the deliveries renderer falls
// back to "-" in that column.
func buildDeliveryIndex(ctx context.Context, c *client.Client, runs []*store.Run) map[string]deliveryHit {
	out := map[string]deliveryHit{}
	for _, r := range runs {
		if r == nil {
			continue
		}
		if r.TriggerSource != "github" {
			// Non-github-triggered runs cannot carry a GITHUB_DELIVERY
			// env var; skip them to avoid the controller round-trip.
			continue
		}
		trig, err := c.GetTrigger(ctx, r.ID)
		if err != nil || trig == nil {
			continue
		}
		guid := trig.TriggerEnv["GITHUB_DELIVERY"]
		if guid == "" {
			continue
		}
		out[guid] = deliveryHit{runID: r.ID, status: r.Status}
	}
	return out
}

func formatDeliveryTime(rfc string) string {
	ts, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		return rfc
	}
	return ts.UTC().Format("2006-01-02 15:04:05")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

type webhookReplayResp struct {
	ID   int64  `json:"id"`
	GUID string `json:"guid"`
}

func runWebhooksReplay(args []string) error {
	fs := flag.NewFlagSet(cmdWebhooksReplay.Path, flag.ContinueOnError)
	repoFlag := fs.String("repo", "", "GitHub repo (OWNER/NAME)")
	hook := fs.Int64("hook", 0, "GitHub hook id")
	delivery := fs.String("delivery", "", "delivery UUID to redeliver")
	on := addProfileFlag(fs)
	_ = on
	if err := parseAndCheck(cmdWebhooksReplay, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if *repoFlag == "" {
		return errors.New("webhooks replay: --repo is required")
	}
	if *hook == 0 {
		return errors.New("webhooks replay: --hook is required")
	}
	if *delivery == "" {
		return errors.New("webhooks replay: --delivery is required")
	}
	if err := ghCLIAvailable(); err != nil {
		return fmt.Errorf("webhooks replay: %w", err)
	}
	repo, err := normalizeRepo(*repoFlag)
	if err != nil {
		return fmt.Errorf("webhooks replay: %w", err)
	}

	// GitHub's redeliver endpoint wants the numeric delivery id, not
	// the GUID. Resolve via a lookup on the delivery GUID -> id. The
	// delivery list endpoint is the only surface that exposes both.
	var deliveries []githubDelivery
	if err := ghAPI(fmt.Sprintf("/repos/%s/hooks/%d/deliveries?per_page=100", repo, *hook), &deliveries); err != nil {
		return fmt.Errorf("webhooks replay: %w", err)
	}
	var target *githubDelivery
	for i := range deliveries {
		if deliveries[i].GUID == *delivery {
			target = &deliveries[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("webhooks replay: delivery %q not found in the most recent 100 deliveries for hook %d", *delivery, *hook)
	}

	var resp webhookReplayResp
	if err := ghAPIPost(fmt.Sprintf("/repos/%s/hooks/%d/deliveries/%d/attempts", repo, *hook, target.ID), &resp); err != nil {
		return fmt.Errorf("webhooks replay: %w", err)
	}
	// GitHub returns 202 with a minimal body. Print the new delivery
	// id (numeric) and GUID if present so the operator can tail it.
	if resp.GUID != "" {
		fmt.Fprintf(os.Stdout, "queued redelivery: guid=%s id=%d\n", resp.GUID, resp.ID)
	} else {
		fmt.Fprintf(os.Stdout, "queued redelivery of %s on hook %d\n", *delivery, *hook)
	}
	return nil
}

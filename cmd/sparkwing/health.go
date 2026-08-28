package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type healthSection struct {
	Name   string               `json:"name"`
	Probes []profileProbeResult `json:"probes"`
}

type healthReport struct {
	Profile  string          `json:"profile"`
	Sections []healthSection `json:"sections"`
	OK       bool            `json:"ok"`

	Warnings int `json:"warnings"`
}

func runHealth(args []string) error {
	fs := flag.NewFlagSet(cmdHealth.Path, flag.ContinueOnError)
	on := fs.String("profile", "", "profile name")
	outputFormat := fs.StringP("output", "o", "", "output format: pretty | json")
	if err := parseAndCheck(cmdHealth, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}

	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	report := healthReport{Profile: prof.Name, OK: true}
	report.Sections = []healthSection{
		{
			Name: "CONNECTIVITY",
			Probes: []profileProbeResult{
				probeController(ctx, prof),
				probeAuth(ctx, prof),
				probeLogs(ctx, prof),
				probeGitcache(ctx, prof),
			},
		},
		{
			Name: "FLEET",
			Probes: []profileProbeResult{
				probeAgentsFleet(ctx, prof),
				probePool(ctx, prof),
			},
		},
		{
			Name: "QUEUE",
			Probes: []profileProbeResult{
				probeStuckTriggers(ctx, prof),
				probeRecentRuns(ctx, prof),
			},
		},
	}

	for _, sec := range report.Sections {
		for _, p := range sec.Probes {
			switch p.Status {
			case "fail":
				report.OK = false
			case "warn":
				report.Warnings++
			}
		}
	}

	if *outputFormat == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return err
		}
		if !report.OK {
			return errors.New("one or more probes failed")
		}
		return nil
	}

	fmt.Fprintf(os.Stdout, "=== sparkwing @ %s ===\n\n", prof.Name)
	for i, sec := range report.Sections {
		fmt.Fprintln(os.Stdout, sec.Name)
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, p := range sec.Probes {
			latency := ""
			if p.LatencyMS > 0 {
				latency = fmt.Sprintf("(%dms)", p.LatencyMS)
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n",
				p.Name, p.Status, orDash(p.Target), strings.TrimSpace(p.Detail), latency)
		}
		_ = tw.Flush()
		if i+1 < len(report.Sections) {
			fmt.Fprintln(os.Stdout)
		}
	}

	fmt.Fprintln(os.Stdout)
	if report.OK {
		summary := "overall: ok"
		if report.Warnings > 0 {
			summary = fmt.Sprintf("overall: ok (%d warning(s))", report.Warnings)
		}
		fmt.Fprintln(os.Stdout, summary)
		return nil
	}
	fmt.Fprintln(os.Stdout, "overall: fail")
	return errors.New("one or more probes failed")
}

func probeAgentsFleet(ctx context.Context, prof *profile.Profile) profileProbeResult {
	r := profileProbeResult{Name: "agents", Target: prof.ControllerURL()}
	if prof.ControllerURL() == "" {
		r.Status = "fail"
		r.Detail = "no controller URL in profile"
		return r
	}
	start := time.Now()
	agents, err := fetchAgents(ctx, prof.ControllerURL(), prof.ControllerToken())
	r.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		r.Status = "fail"
		r.Detail = err.Error()
		return r
	}
	const staleAgentThreshold = 5 * time.Minute
	connected, stale := 0, 0
	for _, a := range agents {
		if a.LastSeen == "" {
			stale++
			continue
		}
		ts, perr := time.Parse(time.RFC3339, a.LastSeen)
		if perr != nil || time.Since(ts) > staleAgentThreshold {
			stale++
			continue
		}
		connected++
	}
	switch {
	case connected == 0 && stale == 0:
		r.Status = "warn"
		r.Detail = "no agents registered"
	case stale > 0:
		r.Status = "warn"
		r.Detail = fmt.Sprintf("%d connected, %d stale (>5m no heartbeat)", connected, stale)
	default:
		r.Status = "ok"
		r.Detail = fmt.Sprintf("%d connected", connected)
	}
	return r
}

func probePool(ctx context.Context, prof *profile.Profile) profileProbeResult {
	r := profileProbeResult{Name: "pool", Target: prof.ControllerURL()}
	if prof.ControllerURL() == "" {
		r.Status = "fail"
		r.Detail = "no controller URL in profile"
		return r
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(prof.ControllerURL(), "/")+"/api/v1/pool", nil)
	if err != nil {
		r.Status = "fail"
		r.Detail = err.Error()
		return r
	}
	if prof.ControllerToken() != "" {
		req.Header.Set("Authorization", "Bearer "+prof.ControllerToken())
	}
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	r.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		r.Status = "fail"
		r.Detail = err.Error()
		return r
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		r.Status = "fail"
		r.Detail = fmt.Sprintf("GET /api/v1/pool -> %s", resp.Status)
		return r
	}
	var out struct {
		Entries []struct {
			Status string `json:"status"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		r.Status = "fail"
		r.Detail = fmt.Sprintf("decode pool response: %v", err)
		return r
	}
	capacity := len(out.Entries)
	inUse, idle := 0, 0
	for _, e := range out.Entries {
		switch e.Status {
		case "in_use", "checked-out", "running":
			inUse++
		case "idle", "ready":
			idle++
		}
	}
	if capacity == 0 {
		r.Status = "warn"
		r.Detail = "pool empty (K8s Runner fallback only)"
		return r
	}
	r.Status = "ok"
	r.Detail = fmt.Sprintf("cap=%d idle=%d in-use=%d", capacity, idle, inUse)
	return r
}

func probeStuckTriggers(ctx context.Context, prof *profile.Profile) profileProbeResult {
	r := profileProbeResult{Name: "triggers", Target: prof.ControllerURL()}
	if prof.ControllerURL() == "" {
		r.Status = "fail"
		r.Detail = "no controller URL in profile"
		return r
	}
	c := client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken())
	start := time.Now()
	triggers, err := c.ListTriggers(ctx, store.TriggerFilter{
		Statuses: []string{"claimed"},
		Limit:    200,
	})
	r.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		r.Status = "fail"
		r.Detail = err.Error()
		return r
	}
	const stuckThreshold = 30 * time.Minute
	stuck := 0
	for _, t := range triggers {
		if t.ClaimedAt == nil || t.ClaimedAt.IsZero() {
			continue
		}
		if time.Since(*t.ClaimedAt) > stuckThreshold {
			stuck++
		}
	}
	if stuck == 0 {
		r.Status = "ok"
		r.Detail = fmt.Sprintf("%d claimed, 0 stuck", len(triggers))
		return r
	}
	r.Status = "warn"
	r.Detail = fmt.Sprintf("%d claimed, %d stuck (claim >30m)", len(triggers), stuck)
	return r
}

func probeRecentRuns(ctx context.Context, prof *profile.Profile) profileProbeResult {
	r := profileProbeResult{Name: "runs (24h)", Target: prof.ControllerURL()}
	if prof.ControllerURL() == "" {
		r.Status = "fail"
		r.Detail = "no controller URL in profile"
		return r
	}
	c := client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken())
	start := time.Now()
	runs, err := c.ListRuns(ctx, store.RunFilter{
		Since: time.Now().Add(-24 * time.Hour),
		Limit: 500,
	})
	r.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		r.Status = "fail"
		r.Detail = err.Error()
		return r
	}
	total := len(runs)
	if total == 0 {
		r.Status = "warn"
		r.Detail = "no runs in last 24h"
		return r
	}
	success, failed, other := 0, 0, 0
	for _, run := range runs {
		switch run.Status {
		case "success":
			success++
		case "failed", "cancelled":
			failed++
		default:
			other++
		}
	}
	rate := 100.0
	if total > 0 {
		rate = float64(success) / float64(total) * 100.0
	}
	detail := fmt.Sprintf("%d total, %.1f%% success (%d failed)", total, rate, failed)
	if other > 0 {
		detail += fmt.Sprintf(", %d in-flight", other)
	}
	if rate < 95.0 {
		r.Status = "warn"
	} else {
		r.Status = "ok"
	}
	r.Detail = detail
	return r
}

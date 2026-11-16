package isolationprobe

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// SiblingJob is the fixture job whose output the probe job reads without depending on it.
const SiblingJob = "output-sibling"

// ProbeJob is the fixture job that runs the probe.
const ProbeJob = "probe"

type route struct {
	name, method, path string
	body               []byte
	waitFinished       bool
}

// safety: customer code holds no controller authority, so every anonymous call must be refused.
func controllerRoutes(plan Plan, runID string) []route {
	if runID == "" {
		return nil
	}
	output := func(run, job string) string {
		return "/api/v1/runs/" + url.PathEscape(run) + "/jobs/" + url.PathEscape(job) + "/output"
	}
	routes := []route{
		{name: "anonymous token list", method: http.MethodGet, path: "/api/v1/tokens"},
		{name: "anonymous own-run source credential", method: http.MethodPost, path: "/api/v1/runs/" + url.PathEscape(runID) + "/source-credential", body: []byte("{}")},
		{name: "anonymous own-run sibling output", method: http.MethodGet, path: output(runID, SiblingJob), waitFinished: true},
	}
	for _, peer := range plan.PeerRuns {
		routes = append(routes, route{name: "anonymous peer-run probe output", method: http.MethodGet, path: output(peer, ProbeJob)})
	}
	return routes
}

func (n network) routeProbes(ctx context.Context, where string, plan Plan, runID string, wait func(context.Context) error) []Result {
	routes := controllerRoutes(plan, runID)
	if len(routes) == 0 {
		return []Result{{Item: "C3", Context: where, Probe: "anonymous controller routes", Expect: ExpectDenied, Outcome: "no run ID outside a Cloud job", Verdict: NotProbed}}
	}
	base := strings.TrimRight(plan.ControllerURL, "/")
	out := make([]Result, 0, len(routes))
	for _, rt := range routes {
		start := n.now()
		code, err := n.status(ctx, rt.method, base+rt.path, rt.body, nil)
		// safety: an unfinished output answers 409 and settles nothing, so the probe waits for the
		// sibling to finish before judging.
		for rt.waitFinished && err == nil && code == http.StatusConflict {
			if werr := wait(ctx); werr != nil {
				break
			}
			code, err = n.status(ctx, rt.method, base+rt.path, rt.body, nil)
		}
		r := Result{Item: "C3", Context: where, Probe: rt.name, Target: rt.method + " " + rt.path, Expect: ExpectDenied, Verdict: gradeVerdict([]int{code}, err)}
		if err != nil {
			r.Outcome = classifyDial(err)
		} else {
			r.Outcome = describeCodes([]int{code}, nil)
		}
		r.Millis = n.now().Sub(start).Milliseconds()
		out = append(out, r)
	}
	return out
}

func waitTicks(interval, budget time.Duration) func(context.Context) error {
	deadline := time.Now().Add(budget)
	return func(ctx context.Context) error {
		if !time.Now().Before(deadline) {
			return context.DeadlineExceeded
		}
		t := time.NewTimer(interval)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			return nil
		}
	}
}

// safety: the probe reports only whether a credential-shaped variable or file exists, never its
// name or value.
var credentialNames = []string{"TOKEN", "SECRET", "PASSWORD", "AWS_", "GITHUB_", "NETRC", "KEY"}

const serviceAccountToken = "/var/run/secrets/kubernetes.io/serviceaccount/token"

func credentialProbes(where string, env []string, home string, readFile func(string) ([]byte, error)) []Result {
	found := false
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		up := strings.ToUpper(key)
		for _, name := range credentialNames {
			found = found || strings.Contains(up, name)
		}
	}
	envResult := Result{Item: "C3", Context: where, Probe: probeCredentialEnv, Expect: ExpectBlocked, Outcome: "none set", Verdict: Pass}
	if found {
		envResult.Outcome, envResult.Verdict = "one or more set", Breach
	}
	out := []Result{envResult}
	files := []string{serviceAccountToken}
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "AWS_WEB_IDENTITY_TOKEN_FILE="); ok && v != "" {
			files = append(files, v)
		}
	}
	if home != "" {
		files = append(files, filepath.Join(home, ".netrc"))
	}
	for _, path := range files {
		_, err := readFile(path)
		r := Result{Item: "C3", Context: where, Probe: "credential file " + filepath.Base(path), Target: path, Expect: ExpectBlocked, Outcome: "readable", Verdict: Breach}
		switch {
		case err == nil:
		case errors.Is(err, fs.ErrNotExist):
			r.Outcome, r.Verdict = "absent", Pass
		case errors.Is(err, fs.ErrPermission):
			r.Outcome, r.Verdict = "permission denied", Pass
		default:
			r.Outcome, r.Verdict = "unreadable", Inconclusive
		}
		out = append(out, r)
	}
	return out
}

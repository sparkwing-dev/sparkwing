package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDeployScanPartialExampleCannotClearFindings(t *testing.T) {
	for _, scenario := range []string{"partial", "marked-partial", "wrong-target", "not-post-deploy", "running", "other-controller", "unavailable", "cannot-start"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFakeDeploy(t)
			d := f.deployer(false)
			started, awaited := false, false
			start := d.tools.startChild
			d.tools.startChild = func(ctx context.Context, name string, args map[string]string) (string, error) {
				started = true
				if scenario == "cannot-start" {
					return "", errors.New("start unavailable")
				}
				return start(ctx, name, args)
			}
			d.tools.awaitChild = func(context.Context, string) (string, error) { awaited = true; return "failed", nil }
			var recorded []map[string]string
			var feedback []string
			d.tools.run = func(ctx context.Context, c deployCommand) (string, error) {
				joined := strings.Join(c.Args, " ")
				if filepath.Base(c.Name) == "sparkwing" && strings.HasPrefix(joined, "runs list") {
					finished := d.tools.now().Add(-time.Minute).Format(time.RFC3339Nano)
					if scenario == "running" {
						finished = "0001-01-01T00:00:00Z"
					}
					return fmt.Sprintf(`{"id":"manual-only-configure","finished_at":%q}`, finished), nil
				}
				if filepath.Base(c.Name) == "sparkwing" && strings.HasPrefix(joined, "runs status") {
					if scenario == "unavailable" {
						return "", errors.New("detail unavailable")
					}
					marker := ""
					if scenario == "marked-partial" || scenario == "wrong-target" || scenario == "not-post-deploy" {
						marker = d.cfg.Production.ControllerURL
					}
					target, postDeploy, eligible := d.cfg.Production.ControllerURL, "true", false
					if scenario == "wrong-target" {
						target, eligible = d.cfg.Staging.ControllerURL, true
					}
					if scenario == "not-post-deploy" {
						postDeploy, eligible = "false", true
					}
					if scenario == "other-controller" {
						marker = d.cfg.Staging.ControllerURL
					}
					return fmt.Sprintf(`{"id":"manual-only-configure","status":"success","inputs":{"proof_eligible":%t,"args":{"scan-receipt":%q,"api-controller":%q,"post-deploy":%q}},"jobs":[{"id":"configure","outcome":"success"}]}`, eligible, marker, target, postDeploy), nil
				}
				if filepath.Base(c.Name) == "bitwing" && len(c.Args) > 1 && c.Args[1] == "record" {
					feedback = slices.Clone(c.Args)
					if i := slices.Index(c.Args, "--file"); i >= 0 {
						body, err := os.ReadFile(c.Args[i+1])
						if err != nil {
							return "", err
						}
						if err = json.Unmarshal(body, &recorded); err != nil {
							return "", err
						}
					}
					return `{}`, nil
				}
				return f.run(ctx, c)
			}
			rec, err := d.postDeployScan(deployScanContext(t), d.cfg.Production, f.candidate(), "deploy-new")
			if err == nil || !started || rec.ExampleRun == "manual-only-configure" {
				t.Fatalf("partial manual run replaced the full scan: reused=%s started=%t err=%v", rec.ExampleRun, started, err)
			}
			if scenario == "cannot-start" {
				if !slices.Contains(feedback, "--could-not-run") || slices.Contains(feedback, "--file") {
					t.Fatalf("unavailable scan cleared findings: %v", feedback)
				}
			} else if !awaited || len(recorded) == 0 {
				t.Fatalf("real scan failure was not observed: awaited=%t findings=%v", awaited, recorded)
			}
		})
	}
}

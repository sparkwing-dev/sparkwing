package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type deployScanObservation struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Failure string `json:"failure"`
	Inputs  struct {
		Args          DeployScanArgs `json:"args"`
		RequestedArgs DeployScanArgs `json:"requested_args"`
	} `json:"inputs"`
	Jobs []struct {
		ID      string `json:"id"`
		Outcome string `json:"outcome"`
		Reason  string `json:"reason"`
		Error   string `json:"error"`
		Attempt int    `json:"attempt"`
	} `json:"jobs"`
}

func (d *deployer) flagProductionScan(ctx context.Context) error {
	const profile = "production-scan-feedback"
	if err := d.addProfile(ctx, profile, d.cfg.Production.ControllerURL, d.cfg.Params.SmokeToken); err != nil {
		return err
	}
	cursor := ""
	for {
		args := []string{"runs", "list", "--profile", profile, "--pipeline", "deploy-scan", "--repo", "sparkwing-dev/sparkwing", "--limit", "1", "-o", "json"}
		if cursor != "" {
			args = append(args, "--cursor", cursor)
		}
		out, err := d.sw(ctx, "", args...)
		if err != nil {
			return err
		}
		decoder := json.NewDecoder(strings.NewReader(out))
		cursor = ""
		for {
			var row struct {
				ID     string `json:"id"`
				Cursor string `json:"next_cursor"`
			}
			if err := decoder.Decode(&row); err == io.EOF {
				break
			} else if err != nil {
				return err
			}
			if row.ID == "" {
				cursor = row.Cursor
				continue
			}
			out, err := d.sw(ctx, "", "runs", "status", "--run", row.ID, "--profile", profile, "--exit-zero", "-o", "json")
			if err != nil {
				return err
			}
			var scan deployScanObservation
			if err := json.Unmarshal([]byte(out), &scan); err != nil {
				return fmt.Errorf("invalid previous scan %s: %w", row.ID, err)
			}
			if scan.ID != row.ID {
				return fmt.Errorf("scan observation returned %s, want %s", scan.ID, row.ID)
			}
			args := scan.Inputs.Args
			if args.Environment == "" {
				args = scan.Inputs.RequestedArgs
			}
			if args.Environment != "production" {
				continue
			}
			message := "Previous production post-deploy scan " + scan.ID + " is " + scan.Status + ": https://console.sparkwing.dev/runs?run=" + scan.ID
			if scan.Status != "success" {
				message = "WARNING: " + message + ". Repair deploy continues."
			}
			d.tools.say(ctx, message)
			sparkwing.Summary(ctx, message)
			// bug: a scan can fail before its job starts, bypassing its own feedback.
			if scan.Status == "failed" || scan.Status == "cancelled" {
				const source = "sparkwing-post-deploy-production-execution"
				report := []map[string]string{}
				for _, job := range scan.Jobs {
					if job.Attempt == 0 && (job.Outcome == "failed" || job.Outcome == "cancelled") {
						report = append(report, map[string]string{"fingerprint": source + "/" + job.ID + "/" + job.Reason, "severity": "high", "title": "Production post-deploy scan execution failed: " + job.ID, "detail": message + "\n" + args.Record + "\n" + job.Error})
					}
				}
				if len(scan.Jobs) == 0 {
					report = append(report, map[string]string{"fingerprint": source + "/run/" + scan.Reason, "severity": "high", "title": "Production post-deploy scan planning failed", "detail": message + "\n" + args.Record + "\n" + scan.Failure})
				}
				if len(report) == 0 {
					return nil
				}
				body, err := json.Marshal(report)
				if err != nil {
					return err
				}
				file := filepath.Join(d.tmp, "scan-execution-findings.json")
				if err := os.WriteFile(file, body, 0o600); err != nil {
					return err
				}
				if _, err := d.cmd(ctx, deployCommand{Name: "bitwing", Args: []string{"finding", "record", "--source", source, "--file", file, "-o", "json"}}); err != nil {
					return err
				}
				path, err := d.tool("bitwing")
				if err != nil {
					return err
				}
				return d.scanTickets(ctx, path, source, report)
			}
			return nil
		}
		if cursor == "" {
			return nil
		}
	}
}

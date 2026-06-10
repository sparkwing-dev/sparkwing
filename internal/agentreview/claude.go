package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// allowedTools is the read-only tool set each reviewer runs with. No
// edit/write tools are granted; in headless mode any tool outside this
// allowlist is auto-denied, so a reviewer can inspect the tree but never
// mutate it.
const allowedTools = "Read,Grep,Glob,Bash(git diff:*),Bash(git log:*),Bash(git show:*),Bash(git status:*),Bash(git blame:*)"

// findingsSchema constrains each reviewer's final output. The orchestrator
// passes it via --json-schema so the model is forced to emit conforming
// JSON (claude retries internally on mismatch).
const findingsSchema = `{"type":"object","additionalProperties":false,"properties":{"findings":{"type":"array","items":{"type":"object","additionalProperties":false,"properties":{"file":{"type":"string"},"line":{"type":"integer"},"severity":{"type":"string","enum":["blocker","high","medium","low"]},"category":{"type":"string"},"claim":{"type":"string"},"suggestion":{"type":"string"}},"required":["file","severity","claim"]}}},"required":["findings"]}`

type claudeEnvelope struct {
	IsError   bool   `json:"is_error"`
	Subtype   string `json:"subtype"`
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
}

// review runs one reviewer headless against the diff and returns its
// findings. It resumes the reviewer's prior session when one exists (so
// the reviewer remembers what it flagged last push and can judge whether
// the new commits addressed it); restart forces a fresh session.
func review(ctx context.Context, bin, root string, ag agentDef, system, user, sessionFile string, restart bool) ([]Finding, error) {
	args := []string{
		"-p",
		"--output-format", "json",
		"--json-schema", findingsSchema,
		"--model", ag.Model,
		"--append-system-prompt", system,
		"--allowedTools", allowedTools,
		"--permission-mode", "default",
	}

	resumeID := ""
	if !restart {
		if data, err := os.ReadFile(sessionFile); err == nil {
			resumeID = strings.TrimSpace(string(data))
		}
	}
	if resumeID != "" {
		args = append(args, "--resume", resumeID)
	} else {
		id, err := newUUID()
		if err != nil {
			return nil, err
		}
		args = append(args, "--session-id", id)
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(user)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("claude exec: %v: %s", err, tail(stderr.String()))
	}

	var env claudeEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		return nil, fmt.Errorf("decode claude envelope: %v: %s", err, tail(stdout.String()))
	}
	if env.IsError {
		return nil, fmt.Errorf("claude reported error (%s): %s", env.Subtype, tail(env.Result))
	}

	if env.SessionID != "" {
		if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err == nil {
			_ = os.WriteFile(sessionFile, []byte(env.SessionID+"\n"), 0o644)
		}
	}

	payload, err := parsePayload(env.Result)
	if err != nil {
		return nil, fmt.Errorf("decode findings: %v: %s", err, tail(env.Result))
	}
	out := make([]Finding, 0, len(payload.Findings))
	for _, f := range payload.Findings {
		if !f.Severity.valid() {
			f.Severity = SevLow
		}
		f.Agent = ag.Name
		out = append(out, f)
	}
	return out, nil
}

// parsePayload decodes the reviewer's structured output, tolerating an
// occasional code fence or surrounding prose the model may add despite
// the schema.
func parsePayload(s string) (agentPayload, error) {
	var p agentPayload
	trimmed := strings.TrimSpace(s)
	if err := json.Unmarshal([]byte(trimmed), &p); err == nil {
		return p, nil
	}
	if i, j := strings.Index(trimmed, "{"), strings.LastIndex(trimmed, "}"); i >= 0 && j > i {
		if err := json.Unmarshal([]byte(trimmed[i:j+1]), &p); err == nil {
			return p, nil
		}
	}
	return p, fmt.Errorf("no JSON object found")
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	const max = 600
	if len(s) > max {
		return "..." + s[len(s)-max:]
	}
	return s
}

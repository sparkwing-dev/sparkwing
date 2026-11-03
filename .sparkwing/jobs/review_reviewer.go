package jobs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type reviewer interface {
	review(ctx context.Context, dir, session, prompt string) (reviewAnswer, error)
}

type reviewAnswer struct {
	session string
	text    string
}

var reviewerModels = map[string]func() reviewer{
	"codex-sol":   func() reviewer { return codexReviewer{model: "gpt-6.1-sol"} },
	"claude-opus": func() reviewer { return claudeReviewer{model: "opus"} },
}

func newReviewer(model string) (reviewer, error) {
	mk, ok := reviewerModels[model]
	if !ok {
		names := make([]string, 0, len(reviewerModels))
		for n := range reviewerModels {
			names = append(names, n)
		}
		return nil, fmt.Errorf("reviewer model %q is not one this job runs (%s)", model, strings.Join(names, ", "))
	}
	return mk(), nil
}

type codexReviewer struct{ model string }

// safety: the sandbox is read-only, so the reviewer can read the repository
// and run git but never change the tree it is judging.
func (c codexReviewer) review(ctx context.Context, dir, session, prompt string) (reviewAnswer, error) {
	args := []string{"exec"}
	if session != "" {
		args = append(args, "resume", session, "--json", "-c", `sandbox_mode="read-only"`, "-m", c.model, "-")
	} else {
		args = append(args, "--json", "-s", "read-only", "-m", c.model, "-C", dir, "-")
	}
	stdout, err := runWithStdin(ctx, dir, prompt, nil, "codex", args...)
	if err != nil {
		return reviewAnswer{}, err
	}
	id, text := codexEvents(stdout)
	if id == "" {
		id = session
	}
	if text == "" {
		return reviewAnswer{}, fmt.Errorf("codex printed no final message: %q", clip(string(stdout), 400))
	}
	return reviewAnswer{session: id, text: text}, nil
}

func codexEvents(jsonl []byte) (thread, lastMessage string) {
	sc := bufio.NewScanner(bytes.NewReader(jsonl))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var ev struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Item     struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch {
		case ev.Type == "thread.started" && thread == "":
			thread = ev.ThreadID
		case ev.Type == "item.completed" && ev.Item.Type == "agent_message":
			lastMessage = ev.Item.Text
		}
	}
	return thread, lastMessage
}

type claudeReviewer struct{ model string }

func (c claudeReviewer) review(ctx context.Context, dir, session, prompt string) (reviewAnswer, error) {
	args := []string{"-p", "--model", c.model, "--output-format", "json", "--tools", "Read,Grep,Glob", "--allowedTools", "Read,Grep,Glob"}
	if session != "" {
		args = append(args, "--resume", session)
	}
	// hack: claude refuses to start inside another Claude Code session, which
	// is how an agent running this pipeline would otherwise invoke it.
	stdout, err := runWithStdin(ctx, dir, prompt, []string{"CLAUDECODE"}, "claude", args...)
	if err != nil {
		return reviewAnswer{}, err
	}
	var out struct {
		IsError   bool   `json:"is_error"`
		Result    string `json:"result"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(stdout, &out); err != nil {
		return reviewAnswer{}, fmt.Errorf("claude printed no JSON result: %w: %q", err, clip(string(stdout), 400))
	}
	if out.IsError {
		return reviewAnswer{}, fmt.Errorf("claude reported an error: %s", clip(out.Result, 400))
	}
	return reviewAnswer{session: out.SessionID, text: out.Result}, nil
}

func runWithStdin(ctx context.Context, dir, stdin string, unset []string, name string, args ...string) ([]byte, error) {
	if _, err := exec.LookPath(name); err != nil {
		return nil, fmt.Errorf("the %s CLI is not on PATH", name)
	}
	// #nosec G204 -- name is a fixed CLI and args are built here
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, u := range unset {
			drop = drop || key == u
		}
		if !drop {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, fmt.Errorf("%s exited %d: %s", name, exit.ExitCode(), clip(strings.TrimSpace(stderr.String()), 800))
		}
		return nil, fmt.Errorf("run %s: %w", name, err)
	}
	return stdout.Bytes(), nil
}

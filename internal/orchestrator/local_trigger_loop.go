package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/retryprovenance"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	TriggerHostVerb      = "__host-trigger"
	SubmitRepoDirKey     = "_SPARKWING_SUBMIT_REPO_DIR"
	PipelineRevKey       = retryprovenance.PipelineRevisionKey
	PipelineDirKey       = "_SPARKWING_SUBMIT_PIPELINE_DIR"
	SubmitPriorityKey    = "_SPARKWING_SUBMIT_PRIORITY"
	childStderrTailBytes = 8 << 10
	childStderrTailLines = 6
)

type stderrTail struct {
	limit int
	buf   []byte
}

func dispatchLocalTrigger(ctx context.Context, trigger *store.Trigger, logger *slog.Logger, environment []string) error {
	host, err := triggerHostBinary()
	if err != nil {
		return err
	}
	logger.Info("local trigger: dispatching shared host", "run_id", trigger.ID)
	return execLocalChild(ctx, host, "", []string{TriggerHostVerb, "--run", trigger.ID}, environment)
}

func triggerHostBinary() (string, error) {
	if paths.UnderTest() {
		if bin := os.Getenv(wingdclient.HostBinEnv); bin != "" {
			return bin, nil
		}
	}
	bin, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve the sparkwing binary that hosts child runs: %w", err)
	}
	return bin, nil
}

func submissionExecutionEnvironment(captured []string, home string) []string {
	if captured == nil {
		captured = os.Environ()
	}
	blocked := map[string]struct{}{
		wingdclient.HostBinEnv: {},
		"SPARKWING_START_AT":   {}, "SPARKWING_STOP_AT": {}, "SPARKWING_ONLY": {},
		"SPARKWING_NO_CACHE": {}, "SPARKWING_DRY_RUN": {},
		"SPARKWING_ALLOW": {}, "SPARKWING_REF": {},
		"SPARKWING_MODE": {}, "SPARKWING_WORKERS": {},
		AdmissionClassEnv:              {},
		"SPARKWING_DEBUG_PAUSE_BEFORE": {}, "SPARKWING_DEBUG_PAUSE_AFTER": {},
		"SPARKWING_DEBUG_PAUSE_ON_FAILURE": {},
		StandaloneStateDBEnv:               {}, StandaloneReasonEnv: {},
	}
	out := make([]string, 0, len(captured)+1)
	for _, entry := range captured {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, denied := blocked[key]; !denied && key != "SPARKWING_HOME" {
			out = append(out, entry)
		}
	}
	if host, _, ok := wingdclient.ResolveHostBin(); ok {
		out = append(out, wingdclient.HostBinEnv+"="+host)
	}
	return append(out, "SPARKWING_HOME="+home)
}

func execLocalChild(ctx context.Context, binPath, repoDir string, args, env []string) error {
	cmd := exec.Command(binPath, args...)
	cmd.Dir = repoDir
	cmd.Stdout = os.Stdout
	tail := &stderrTail{limit: childStderrTailBytes}
	cmd.Stderr = io.MultiWriter(os.Stderr, tail)
	cmd.Env = env
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	outcome, startErr := runAssistedChildProcess(ctx, cmd, nil)
	if err := errors.Join(startErr, outcome.waitErr, outcome.cancelCause); err != nil {
		if reason := tail.lastLines(childStderrTailLines); reason != "" {
			return fmt.Errorf("child exec: %w: %s", err, reason)
		}
		return fmt.Errorf("child exec: %w", err)
	}
	return nil
}

func (t *stderrTail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = t.buf[len(t.buf)-t.limit:]
	}
	return len(p), nil
}

func (t *stderrTail) lastLines(n int) string {
	lines := strings.Split(strings.TrimSpace(string(t.buf)), "\n")
	kept := lines[:0]
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			kept = append(kept, l)
		}
	}
	if len(kept) > n {
		kept = kept[len(kept)-n:]
	}
	return strings.Join(kept, " | ")
}

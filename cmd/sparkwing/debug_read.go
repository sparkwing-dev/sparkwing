package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

type debugObservation struct {
	controllerwire.DebugJob
	Pause *controllerwire.DebugPause `json:"pause"`
}

func runDebugRead(ctx context.Context, inv clisurface.Invocation) error {
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, inv.Output(color.IsInteractiveStdout()))
	}
	options, closeTransport, err := controllerOptions(ctx, inv.Value("profile"))
	if err != nil {
		return err
	}
	defer closeTransport()
	reader, err := client.NewDebugReader(options)
	if err != nil {
		return err
	}
	return observeDebug(ctx, inv, reader, os.Stdout, attachDebugExecution)
}

func observeDebug(ctx context.Context, inv clisurface.Invocation, reader controllerwire.DebugReader, out io.Writer, attach func(context.Context, controllerwire.DebugExecution, io.Writer) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	job, err := reader.ReadDebugJob(ctx, inv.Value("run"), inv.Value("job"))
	if err != nil {
		return err
	}
	if err := job.Validate(inv.Value("run"), inv.Value("job")); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if inv.Command.Path == "sparkwing debug attach" {
		return attachObservedDebug(ctx, inv, job, out, attach)
	}
	pause, err := reader.ReadDebugPause(ctx, job.RunID, job.Job.ID)
	if err != nil {
		return err
	}
	if err := pause.Validate(job.RunID, job.Job.ID); err != nil {
		return err
	}
	if pause.Number != job.Number {
		return invalidObservation("debug observations disagree on the run number")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return renderDebugObservation(out, debugObservation{DebugJob: job, Pause: pause.Pause}, inv.Output(color.IsInteractiveStdout()))
}

func attachObservedDebug(ctx context.Context, inv clisurface.Invocation, job controllerwire.DebugJob, out io.Writer, attach func(context.Context, controllerwire.DebugExecution, io.Writer) error) error {
	if job.Execution == nil || job.Job.Status != "running" {
		return &apierr.Error{Code: apierr.IllegalTransition, Message: "job has no running executor to attach to"}
	}
	if job.Execution.Kind != "kubernetes" {
		return &apierr.Error{Code: apierr.CapabilityUnsupported, Message: "job executor has no Kubernetes attachment target", Hint: "use sparkwing debug env to inspect the job"}
	}
	target := *job.Execution
	if inv.Has("namespace") {
		target.Namespace = inv.Value("namespace")
	}
	if err := target.Validate(); err != nil {
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "invalid Kubernetes attachment namespace"}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	format := inv.Output(color.IsInteractiveStdout())
	if format == "json" {
		if err := json.NewEncoder(out).Encode(target); err != nil {
			return err
		}
	}
	return attach(ctx, target, debugOutputWriter{out: out, format: format})
}

func attachDebugExecution(ctx context.Context, target controllerwire.DebugExecution, out io.Writer) error {
	if err := target.Validate(); err != nil {
		return err
	}
	if target.Kind != "kubernetes" {
		return &apierr.Error{Code: apierr.CapabilityUnsupported, Message: "executor has no Kubernetes attachment target"}
	}
	bin, err := exec.LookPath("kubectl")
	if err != nil {
		return fmt.Errorf("find kubectl: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	env, err := procenv.Build(procenv.CLI, procenv.Base{Local: true, Captured: os.Environ()}, procenv.Runtime{})
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, "exec", "-it", "--namespace", target.Namespace, "--container", target.Container, target.Pod, "--", "bash")
	cmd.Stdin, cmd.Stdout, cmd.Stderr, cmd.Env = os.Stdin, out, os.Stderr, env
	return errors.Join(cmd.Run(), ctx.Err())
}

type debugOutputWriter struct {
	out    io.Writer
	format string
}

func (w debugOutputWriter) Write(data []byte) (int, error) {
	if err := writeText(w.out, "debug_output", string(data), w.format); err != nil {
		return 0, err
	}
	return len(data), nil
}

func renderDebugObservation(out io.Writer, value debugObservation, format string) error {
	if format == "json" {
		return json.NewEncoder(out).Encode(value)
	}
	_, err := fmt.Fprintf(out, "run: %s\njob: %s\nstatus: %s\nattempt: %d\n", value.RunID, value.Job.ID, value.Job.Status, value.Job.Attempt)
	if err != nil {
		return err
	}
	if value.Execution != nil {
		if _, err := fmt.Fprintf(out, "runner: %s (%s)\n", value.Execution.Runner, value.Execution.Kind); err != nil {
			return err
		}
	}
	if value.Job.StatusDetail != "" {
		if _, err := fmt.Fprintf(out, "detail: %s\n", value.Job.StatusDetail); err != nil {
			return err
		}
	}
	if value.Pause == nil {
		_, err = fmt.Fprintln(out, "paused: no active pause")
		return err
	}
	expires := "never"
	if value.Pause.ExpiresAt != nil {
		expires = value.Pause.ExpiresAt.Format("2006-01-02T15:04:05Z07:00")
	}
	_, err = fmt.Fprintf(out, "paused: %s (reason=%s, expires=%s)\n", value.Pause.PausedAt.Format("2006-01-02T15:04:05Z07:00"), value.Pause.Reason, expires)
	return err
}

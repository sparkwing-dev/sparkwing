package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type queueDependencies struct {
	connect    func(context.Context, string) (controllerwire.QueueManager, func(), error)
	priority   func(context.Context, string, int, string) (queuePriorityResult, error)
	resolveRun func(context.Context, string) (string, error)
}

func runQueue(args []string) error {
	return runQueueCommand(context.Background(), args, os.Stdout, queueDependencies{
		connect:    resolveQueueManager,
		resolveRun: resolveQueueRun,
		priority: func(ctx context.Context, run string, priority int, mode string) (queuePriorityResult, error) {
			return applyQueuePriority(ctx, "", run, priority, mode)
		},
	})
}

func resolveQueueManager(ctx context.Context, profile string) (controllerwire.QueueManager, func(), error) {
	options, closeTransport, err := controllerOptions(ctx, profile)
	if err != nil {
		return nil, nil, err
	}
	manager, err := client.NewQueueManager(options)
	if err != nil {
		closeTransport()
		return nil, nil, err
	}
	return manager, closeTransport, nil
}

func runQueueCommand(ctx context.Context, args []string, out io.Writer, deps queueDependencies) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"queue"}, args...))
	if err != nil {
		return err
	}
	format := inv.Output(color.IsInteractiveStdout())
	if inv.Help() || inv.Command.Path == "sparkwing queue" {
		return writeCommandHelp(surfaceHelp(inv.Command), out, format)
	}
	group := inv.Value("group")
	if inv.Has("group") {
		if err := controllerwire.ValidateQueueGroupName(group); err != nil {
			return &clisurface.UsageError{Path: inv.Command.Path, Message: err.Error()}
		}
	}
	input, err := queuePriorityInput(inv)
	if err != nil {
		return err
	}
	timeout := 5 * time.Second
	if inv.Value("profile") != "" {
		timeout = 30 * time.Second
	}
	if inv.Command.Path == "sparkwing queue priority" {
		timeout = queuePriorityTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if inv.Command.Path == "sparkwing queue priority" && group == "" {
		if strings.Trim(input.RunID, "0123456789") == "" {
			input.RunID, err = deps.resolveRun(ctx, input.RunID)
			if err != nil {
				return err
			}
			if err := input.Validate(); err != nil {
				return &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned an invalid run identity"}
			}
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		priority := 0
		if input.Priority != nil {
			priority = *input.Priority
		}
		result, err := deps.priority(ctx, input.RunID, priority, input.Mode)
		if err != nil {
			return err
		}
		return writeQueuePriority(out, result, format)
	}
	manager, closeTransport, err := deps.connect(ctx, inv.Value("profile"))
	if err != nil {
		return err
	}
	defer closeTransport()
	if err := ctx.Err(); err != nil {
		return err
	}
	return manageQueue(ctx, inv, input, manager, out, format)
}

func resolveQueueRun(ctx context.Context, selector string) (string, error) {
	reader, closeTransport, err := resolveRunReader(ctx, "")
	if err != nil {
		return "", err
	}
	defer closeTransport()
	run, err := reader.ReadRun(ctx, selector)
	if err != nil {
		return "", err
	}
	return run.ID, nil
}

func manageQueue(ctx context.Context, inv clisurface.Invocation, input controllerwire.QueuePriorityWrite, manager controllerwire.QueueManager, out io.Writer, format string) error {
	group := inv.Value("group")
	if inv.Command.Path == "sparkwing queue priority" {
		result, err := manager.SetGroupPriority(ctx, group, input)
		if err != nil {
			return err
		}
		return writeQueuePriority(out, result, format)
	}
	if group != "" {
		result, err := manager.ReadQueueGroup(ctx, group)
		if err != nil {
			return err
		}
		return renderQueueGroup(out, result, format)
	}
	result, err := manager.ReadQueue(ctx)
	if err != nil {
		return err
	}
	// safety: buffer the shared renderer because its text paths do not return write failures.
	var buffer bytes.Buffer
	if err := renderQueue(&buffer, result, format); err != nil {
		return err
	}
	_, err = out.Write(buffer.Bytes())
	return err
}

func writeQueuePriority(out io.Writer, result queuePriorityResult, format string) error {
	if !result.Found {
		return renderQueuePriorityMissing(out, format, result)
	}
	return renderQueuePriority(out, result, format)
}

func renderQueueGroup(out io.Writer, group controllerwire.QueueGroup, format string) error {
	if format == "json" {
		return json.NewEncoder(out).Encode(group)
	}
	var buffer bytes.Buffer
	var rows io.Writer = &buffer
	var tw *tabwriter.Writer
	if format == "pretty" {
		tw = tabwriter.NewWriter(&buffer, 0, 0, 2, ' ', 0)
		rows = tw
	}
	fmt.Fprintf(rows, "group\t%s\tcapacity\t%d\teffective\t%d\tused\t%d\n", group.Group, group.Capacity, group.EffectiveCapacity, group.UsedCost)
	if format == "pretty" {
		fmt.Fprintf(rows, "scope\t%s\n", nodehost.ScopeLabelFromKey(group.Group))
		fmt.Fprintln(rows, "STATE\tRUN\tJOB\tPOSITION\tCOST\tSUPERSEDED\tHOLDER / POLICY\tCLAIMED / ARRIVED\tLEASE EXPIRES")
	}
	for _, h := range group.Holders {
		fmt.Fprintf(rows, "holder\t%s\t%s\t-\t%d\t%t\t%s\t%s\t%s\n", h.RunID, h.JobID, h.Cost, h.Superseded, h.HolderID, fmtQueueTime(h.ClaimedAt), fmtQueueTime(h.LeaseExpiresAt))
	}
	for _, w := range group.Waiters {
		fmt.Fprintf(rows, "waiter\t%s\t%s\t%d\t%d\t-\t%s\t%s\t-\n", w.RunID, w.JobID, w.Position, w.Cost, w.Policy, fmtQueueTime(w.ArrivedAt))
	}
	if tw != nil {
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	_, err := out.Write(buffer.Bytes())
	return err
}

func fmtQueueTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}

func renderQueue(w io.Writer, qs wingwire.QueueState, format string) error {
	return opsview.RenderQueue(w, qs, format)
}

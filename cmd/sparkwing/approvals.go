package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/ndjson"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func resolveRunReview(ctx context.Context, profile string) (controllerwire.RunReview, func(), error) {
	options, closeTransport, err := controllerOptions(ctx, profile)
	if err != nil {
		return nil, nil, err
	}
	review, err := client.NewRunReview(options)
	if err != nil {
		closeTransport()
		return nil, nil, err
	}
	return review, closeTransport, nil
}

func runReviewCommand(ctx context.Context, args []string, out io.Writer, resolve func(context.Context, string) (controllerwire.RunReview, func(), error)) error {
	inv, err := clisurface.Parse("sparkwing", args)
	if err != nil {
		return err
	}
	format := inv.Output(color.IsInteractiveStdout())
	if inv.Help() || inv.Command.Path == cmdAnnotations.Path || inv.Command.Path == cmdApprovals.Path {
		return writeCommandHelp(surfaceHelp(inv.Command), out, format)
	}
	if inv.Command.Path == cmdAnnotationsAdd.Path && inv.Value("job") == "" {
		return &apierr.Error{Code: apierr.PolicyMissing, Message: "run-scoped annotations are unavailable", Hint: "use --job to annotate a job or one of its steps"}
	}
	review, closeTransport, err := resolve(ctx, inv.Value("profile"))
	if err != nil {
		return err
	}
	defer closeTransport()
	switch inv.Command.Path {
	case cmdAnnotationsList.Path:
		return writeAnnotationList(ctx, review, inv, out, format)
	case cmdAnnotationsAdd.Path:
		return writeAnnotation(ctx, review, inv, out, format)
	case cmdApprovalsList.Path:
		return writeApprovals(ctx, review, inv, out, format)
	case cmdApprove.Path, cmdDeny.Path:
		return writeApprovalDecision(ctx, review, inv, out, format)
	default:
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "review subcommand required"}
	}
}

func writeApprovals(ctx context.Context, review controllerwire.RunReview, inv clisurface.Invocation, out io.Writer, format string) error {
	result, err := review.ListApprovals(ctx, inv.Value("run"))
	if err != nil {
		return err
	}
	if format == "json" {
		return ndjson.Write(out, result.Approvals)
	}
	if format == "plain" {
		for _, approval := range result.Approvals {
			if _, err := fmt.Fprintf(out, "%s/%s\n", approval.RunID, approval.JobID); err != nil {
				return err
			}
		}
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN\tJOB\tSTATUS\tREQUESTED\tPOLICY\tAPPROVER\tCOMMENT\tMESSAGE")
	for _, approval := range result.Approvals {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", approval.RunID, approval.JobID, approvalStatus(approval), approval.RequestedAt.Format(time.RFC3339), orDashApproval(approval.OnTimeout), orDashApproval(approval.Approver), orDashApproval(truncateOneLine(approval.Comment, 40)), truncateOneLine(approval.Message, 60))
	}
	return tw.Flush()
}

func writeApprovalDecision(ctx context.Context, review controllerwire.RunReview, inv clisurface.Invocation, out io.Writer, format string) error {
	resolution := "approved"
	if inv.Command.Path == cmdDeny.Path {
		resolution = "denied"
	}
	result, err := review.ResolveApproval(ctx, inv.Value("run"), inv.Value("job"), controllerwire.ApprovalDecision{Resolution: resolution, Comment: inv.Value("comment"), IdempotencyKey: rand.Text()})
	if err != nil {
		return err
	}
	if format == "json" {
		return json.NewEncoder(out).Encode(result)
	}
	if format == "plain" {
		_, err = fmt.Fprintf(out, "%s/%s\n", result.Approval.RunID, result.Approval.JobID)
		return err
	}
	_, err = fmt.Fprintf(out, "%s %s/%s by %s\n", result.Approval.Resolution, result.Approval.RunID, result.Approval.JobID, result.Approval.Approver)
	return err
}

func approvalStatus(approval controllerwire.Approval) string {
	if approval.ResolvedAt == nil {
		return "pending"
	}
	return approval.Resolution
}

func orDashApproval(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

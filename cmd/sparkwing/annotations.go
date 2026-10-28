package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/ndjson"
)

type annotationEntry struct {
	RunID  string `json:"run_id"`
	JobID  string `json:"job_id"`
	StepID string `json:"step_id,omitempty"`
	nodeproto.Annotation
}

func listAnnotations(ctx context.Context, review controllerwire.RunReview, inv clisurface.Invocation) ([]annotationEntry, error) {
	run, err := review.ReadRun(ctx, inv.Value("run"))
	if err != nil {
		return nil, err
	}
	if run.ID == "" {
		return nil, invalidObservation("run observation is missing its identity")
	}
	var entries []annotationEntry
	jobFilter, stepFilter := inv.Value("job"), inv.Value("step")
	if stepFilter == "" {
		for _, job := range run.Jobs {
			if jobFilter != "" && job.ID != jobFilter {
				continue
			}
			for _, annotation := range job.Annotations {
				entries = append(entries, annotationEntry{RunID: run.ID, JobID: job.ID, Annotation: annotation})
			}
		}
	}
	if !inv.Bool("steps") && stepFilter == "" {
		return entries, nil
	}
	steps, err := review.ReadSteps(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	for _, step := range steps.Steps {
		if jobFilter != "" && step.JobID != jobFilter || stepFilter != "" && step.ID != stepFilter {
			continue
		}
		for _, annotation := range step.Annotations {
			entries = append(entries, annotationEntry{RunID: run.ID, JobID: step.JobID, StepID: step.ID, Annotation: annotation})
		}
	}
	return entries, nil
}

func writeAnnotationList(ctx context.Context, review controllerwire.RunReview, inv clisurface.Invocation, out io.Writer, format string) error {
	entries, err := listAnnotations(ctx, review, inv)
	if err != nil {
		return err
	}
	if format == "json" {
		return ndjson.Write(out, entries)
	}
	if format == "plain" {
		for _, entry := range entries {
			if _, err := fmt.Fprintln(out, strings.NewReplacer("\r", " ", "\n", " ").Replace(entry.Message)); err != nil {
				return err
			}
		}
		return nil
	}
	if len(entries) == 0 {
		_, err := fmt.Fprintln(out, "no annotations on this run")
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "JOB\tSTEP\tLEVEL\tFILE\tLINE\tMESSAGE")
	for _, entry := range entries {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\n", entry.JobID, orDashApproval(entry.StepID), entry.Level, orDashApproval(entry.File), entry.Line, entry.Message)
	}
	return tw.Flush()
}

func writeAnnotation(ctx context.Context, review controllerwire.RunReview, inv clisurface.Invocation, out io.Writer, format string) error {
	input := controllerwire.AnnotationWrite{Annotation: nodeproto.Annotation{Level: "info", Message: inv.Value("message")}, StepID: inv.Value("step")}
	if err := review.AppendAnnotation(ctx, inv.Value("run"), inv.Value("job"), input); err != nil {
		return err
	}
	record := struct {
		RunID  string `json:"run_id"`
		JobID  string `json:"job_id"`
		StepID string `json:"step_id,omitempty"`
	}{inv.Value("run"), inv.Value("job"), input.StepID}
	if format == "json" {
		return json.NewEncoder(out).Encode(record)
	}
	subject := record.RunID + "/" + record.JobID
	if record.StepID != "" {
		subject += "/" + record.StepID
	}
	if format == "plain" {
		_, err := fmt.Fprintln(out, subject)
		return err
	}
	_, err := fmt.Fprintf(out, "annotated %s\n", subject)
	return err
}

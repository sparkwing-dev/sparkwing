package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

func validateRunArtifacts(inv clisurface.Invocation) error {
	if inv.Has("path") != inv.Has("dest") || inv.Has("path") && !inv.Has("job") {
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "a download requires --job, --path and --dest together"}
	}
	return nil
}

func runRunsArtifacts(ctx context.Context, inv clisurface.Invocation) error {
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, inv.Output(color.IsInteractiveStdout()))
	}
	if err := validateRunArtifacts(inv); err != nil {
		return err
	}
	options, closeTransport, err := controllerOptions(ctx, inv.Value("profile"))
	if err != nil {
		return observationError(err)
	}
	defer closeTransport()
	reader, err := client.NewArtifactReader(options)
	if err != nil {
		return observationError(err)
	}
	return observeRunArtifacts(ctx, inv, reader, os.Stdout)
}

type artifactRecord struct {
	blob.ManifestEntry
	RunID   string `json:"run_id"`
	JobID   string `json:"job_id"`
	SavedTo string `json:"saved_to,omitempty"`
}

func observeRunArtifacts(ctx context.Context, inv clisurface.Invocation, reader client.ArtifactReader, out io.Writer) (err error) {
	defer func() { err = observationError(err) }()
	if err := validateRunArtifacts(inv); err != nil {
		return err
	}
	run, job := inv.Value("run"), inv.Value("job")
	detail, err := reader.ReadRun(ctx, run)
	if err != nil {
		return err
	}
	if detail.ID == "" {
		return invalidObservation("run observation is missing its identity")
	}
	run = detail.ID
	if path := inv.Value("path"); path != "" {
		dest := inv.Value("dest")
		file, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		complete := false
		defer func() {
			if !complete {
				_ = file.Close()
				err = errors.Join(err, os.Remove(dest))
			}
		}()
		entry, readErr := reader.DownloadArtifact(ctx, run, job, path, file)
		if err := errors.Join(readErr, file.Close()); err != nil {
			return err
		}
		complete = true
		return writeArtifact(out, artifactRecord{ManifestEntry: entry, RunID: run, JobID: job, SavedTo: dest}, inv.Output(color.IsInteractiveStdout()))
	}
	jobs := []string{job}
	if job == "" {
		jobs = nil
		for _, row := range detail.Jobs {
			jobs = append(jobs, row.ID)
		}
	}
	for _, job := range jobs {
		entries, err := reader.ReadArtifacts(ctx, run, job)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := writeArtifact(out, artifactRecord{ManifestEntry: entry, RunID: run, JobID: job}, inv.Output(color.IsInteractiveStdout())); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeArtifact(out io.Writer, record artifactRecord, format string) error {
	if format == "json" {
		return json.NewEncoder(out).Encode(record)
	}
	if format == "plain" && record.SavedTo == "" {
		_, err := fmt.Fprintln(out, record.Path)
		return err
	}
	if record.SavedTo != "" {
		_, err := fmt.Fprintln(out, record.SavedTo)
		return err
	}
	_, err := fmt.Fprintf(out, "%s\t%s\t%d\t%s\n", record.JobID, record.Path, record.Size, record.Digest)
	return err
}

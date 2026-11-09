package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"unicode"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

func validateRunArtifacts(inv clisurface.Invocation) error {
	if inv.Has("path") != inv.Has("dest") || inv.Has("path") && !inv.Has("job") {
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "a download requires --job, --path and --dest together"}
	}
	if inv.Has("path") && (inv.Has("limit") || inv.Has("cursor")) {
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "--limit and --cursor apply to listings only"}
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
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
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
		if err := artifactDestinationAvailable(dest); err != nil {
			return err
		}
		file, err := os.CreateTemp(filepath.Dir(dest), ".sparkwing-artifact-*")
		if err != nil {
			return err
		}
		defer func() {
			_ = file.Close()
			if removeErr := os.Remove(file.Name()); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				err = errors.Join(err, removeErr)
			}
		}()
		entry, readErr := reader.DownloadArtifact(ctx, run, job, path, file)
		if err := errors.Join(readErr, file.Close(), ctx.Err()); err != nil {
			return err
		}
		if err := publishArtifactFile(file.Name(), dest); err != nil {
			return err
		}
		return writeArtifact(out, artifactRecord{ManifestEntry: entry, RunID: run, JobID: job, SavedTo: dest}, inv.Output(color.IsInteractiveStdout()))
	}
	return listRunArtifacts(ctx, inv, reader, detail, out)
}

type artifactCursor struct {
	Run    string `json:"run"`
	Filter string `json:"filter"`
	Job    string `json:"job"`
	After  string `json:"after"`
}

type artifactListingPage struct {
	Kind       string `json:"kind"`
	Returned   int    `json:"returned"`
	Limit      int    `json:"limit"`
	Truncated  bool   `json:"truncated"`
	NextCursor string `json:"next_cursor,omitempty"`
}

func listRunArtifacts(ctx context.Context, inv clisurface.Invocation, reader client.ArtifactReader, detail controllerwire.RunDetail, out io.Writer) error {
	job := inv.Value("job")
	limit := 40
	if inv.Has("limit") {
		parsed, err := strconv.Atoi(inv.Value("limit"))
		if err != nil {
			return fmt.Errorf("--limit must be a positive integer")
		}
		limit = parsed
	}
	if limit < 1 {
		return fmt.Errorf("--limit must be at least one")
	}
	jobs := []string{job}
	if job == "" {
		jobs = nil
		for _, row := range detail.Jobs {
			jobs = append(jobs, row.ID)
		}
	}
	slices.Sort(jobs)
	position := artifactCursor{Run: detail.ID, Filter: job}
	if raw := inv.Value("cursor"); raw != "" {
		body, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || json.Unmarshal(body, &position) != nil || position.Run != detail.ID || position.Filter != job || !slices.Contains(jobs, position.Job) {
			return fmt.Errorf("--cursor must come from this run with the same filters")
		}
	}
	page := artifactListingPage{Kind: "page", Limit: limit}
	mode := inv.Output(color.IsInteractiveStdout())
	for _, job := range jobs {
		if job < position.Job {
			continue
		}
		position.Job = job
		for {
			remaining := limit - page.Returned
			entries, err := reader.ReadArtifacts(ctx, detail.ID, job, min(max(remaining, 1), 200), position.After)
			if err != nil {
				return err
			}
			if remaining == 0 && len(entries.Artifacts) > 0 {
				body, err := json.Marshal(position)
				if err != nil {
					return err
				}
				page.Truncated, page.NextCursor = true, base64.RawURLEncoding.EncodeToString(body)
				return writeArtifactPage(out, page, mode)
			}
			for _, entry := range entries.Artifacts {
				if err := writeArtifact(out, artifactRecord{ManifestEntry: entry, RunID: detail.ID, JobID: job}, mode); err != nil {
					return err
				}
				page.Returned++
			}
			position.After = entries.NextCursor
			if !entries.Truncated {
				break
			}
		}
	}
	return writeArtifactPage(out, page, mode)
}

func writeArtifactPage(out io.Writer, page artifactListingPage, mode string) error {
	if mode == "json" {
		return json.NewEncoder(out).Encode(page)
	}
	if mode == "plain" {
		out = os.Stderr
	}
	if page.Truncated {
		_, err := fmt.Fprintf(out, "%d artifacts shown; continue with the same filters and --cursor %q\n", page.Returned, page.NextCursor)
		return err
	}
	if mode == "pretty" {
		_, err := fmt.Fprintf(out, "%d artifacts shown\n", page.Returned)
		return err
	}
	return nil
}

func publishArtifactFile(source, dest string) error {
	// safety: linking a verified sibling file creates the destination atomically without replacing another writer.
	return os.Link(source, dest)
}

func writeArtifact(out io.Writer, record artifactRecord, format string) error {
	if format == "json" {
		return json.NewEncoder(out).Encode(record)
	}
	if format == "plain" && record.SavedTo == "" {
		_, err := fmt.Fprintln(out, artifactDisplayText(record.Path))
		return err
	}
	if record.SavedTo != "" {
		_, err := fmt.Fprintln(out, artifactDisplayText(record.SavedTo))
		return err
	}
	_, err := fmt.Fprintf(out, "%s\t%s\t%d\t%s\n", artifactDisplayText(record.JobID), artifactDisplayText(record.Path), record.Size, record.Digest)
	return err
}

func artifactDestinationAvailable(dest string) error {
	_, err := os.Lstat(dest)
	if err == nil {
		return &os.PathError{Op: "create", Path: dest, Err: os.ErrExist}
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func artifactDisplayText(value string) string {
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return strconv.Quote(value)
	}
	return value
}

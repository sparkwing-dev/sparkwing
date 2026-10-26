package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/pkg/color"

	"github.com/sparkwing-dev/sparkwing/internal/ndjson"

	"github.com/sparkwing-dev/sparkwing/internal/repos"
)

func listCheckouts(outputFormat string, withPipelines bool) error {
	entries, err := repos.List()
	if err != nil {
		return err
	}
	type rowOut struct {
		Path      string   `json:"path"`
		Status    string   `json:"status"`
		Worktree  bool     `json:"worktree,omitempty"`
		Pipelines []string `json:"pipelines,omitempty"`
	}
	rows := make([]rowOut, 0, len(entries))
	for _, e := range entries {
		row := rowOut{Path: e.Path, Status: e.Status, Worktree: e.Worktree}
		if withPipelines && e.Status == "ok" {
			if pipes, perr := repos.PipelineNamesForRepo(e.Path); perr == nil {
				sort.Strings(pipes)
				row.Pipelines = pipes
			}
		}
		rows = append(rows, row)
	}

	if outputFormat == "json" {
		return ndjson.Write(os.Stdout, rows)
	}

	if outputFormat == "plain" {
		for _, row := range rows {
			if _, err := fmt.Fprintln(os.Stdout, row.Path); err != nil {
				return err
			}
		}
		return nil
	}
	if len(rows) == 0 {
		fmt.Println("no repos registered")
		fmt.Println("(register with `sparkwing repos add --checkout <path>` or just run `sparkwing run` in a .sparkwing/-bearing repo)")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tPATH\tPIPELINES")
	for _, r := range rows {
		status := r.Status
		if r.Worktree {
			status += " (worktree)"
		}
		pipelinesCol := strings.Join(r.Pipelines, ", ")
		if !withPipelines {
			pipelinesCol = "-"
		}
		if pipelinesCol == "" {
			pipelinesCol = "(describe failed)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", status, r.Path, pipelinesCol)
	}
	return tw.Flush()
}

func runXrepoAdd(inv clisurface.Invocation) error {
	target, err := invocationCheckout(inv)
	if err != nil {
		return err
	}
	if err := repos.Add(target); err != nil {
		return err
	}
	return writeRepositoryMutation(inv, "registered", target, 1)
}

func runXrepoRemove(inv clisurface.Invocation) error {
	target, err := invocationCheckout(inv)
	if err != nil {
		return err
	}
	count, err := repos.Remove(target)
	if err != nil {
		return err
	}
	return writeRepositoryMutation(inv, "removed", target, count)
}

func runXrepoPrune(inv clisurface.Invocation) error {
	dropped, err := repos.Prune()
	if err != nil {
		return err
	}
	if inv.Output(color.IsInteractiveStdout()) == "json" {
		return json.NewEncoder(os.Stdout).Encode(struct {
			Paths []string `json:"paths"`
		}{dropped})
	}
	for _, path := range dropped {
		if _, err := fmt.Fprintln(os.Stdout, path); err != nil {
			return err
		}
	}
	return nil
}

func invocationCheckout(inv clisurface.Invocation) (string, error) {
	if !inv.Has("checkout") {
		return "", &clisurface.UsageError{Path: inv.Command.Path, Message: "--checkout is required"}
	}
	return os.Getwd()
}

func writeRepositoryMutation(inv clisurface.Invocation, action, path string, count int) error {
	if inv.Output(color.IsInteractiveStdout()) == "json" {
		return json.NewEncoder(os.Stdout).Encode(struct {
			Action string `json:"action"`
			Path   string `json:"path"`
			Count  int    `json:"count"`
		}{action, path, count})
	}
	_, err := fmt.Fprintln(os.Stdout, path)
	return err
}

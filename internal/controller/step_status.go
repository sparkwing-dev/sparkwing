package controller

import (
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func publicRunSteps(rows []*store.NodeStep) controllerwire.RunSteps {
	result := controllerwire.RunSteps{Steps: make([]controllerwire.StepDetail, 0, len(rows))}
	for _, row := range rows {
		step := controllerwire.StepDetail{
			JobID: row.NodeID, ID: row.StepID, Attempt: row.Attempt, Status: row.Status,
			Reason: row.Error, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, Summary: row.Summary,
			Annotations: publicAnnotations(row.Annotations),
		}
		if row.Status != "running" {
			step.Outcome = row.Status
			if row.Status == "passed" {
				step.Outcome = "success"
			}
		}
		result.Steps = append(result.Steps, step)
	}
	return result
}

func publicAnnotations(rows []store.Annotation) []controllerwire.Annotation {
	var out []controllerwire.Annotation
	for _, row := range rows {
		annotation := controllerwire.Annotation{Level: row.Level, Message: row.Message, File: row.File}
		if row.Line != nil {
			annotation.Line = *row.Line
		}
		out = append(out, annotation)
	}
	return out
}

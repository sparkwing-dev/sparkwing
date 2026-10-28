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
			Annotations: row.Annotations,
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

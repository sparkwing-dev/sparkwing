package controller

import (
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func hostSnapshot(snapshot store.RunSnapshot) runstate.RunSnapshot {
	row := snapshot.Run
	return runstate.RunSnapshot{Run: runstate.RunRow{
		ID:                row.ID,
		Number:            row.Number,
		Team:              string(row.Team),
		Pipeline:          row.Pipeline,
		State:             row.State,
		Request:           row.Request,
		Lineage:           row.Lineage,
		Retry:             row.Retry,
		Spec:              row.Spec,
		Plan:              row.Plan,
		PlanHash:          row.PlanHash,
		Dispatch:          row.Dispatch,
		AllowedPools:      row.AllowedPools,
		Owner:             row.Owner,
		CreatedAt:         row.CreatedAt,
		CancelRequestedAt: row.CancelRequestedAt,
		CancelReason:      row.CancelReason,
		StartedAt:         row.StartedAt,
		FinishedAt:        row.FinishedAt,
		StateSeq:          row.StateSeq,
		StateDigest:       row.StateDigest,
		StateActor:        row.StateActor,
	}, Nodes: snapshot.Nodes, Expansions: snapshot.Expansions, StoreNow: snapshot.StoreNow}
}

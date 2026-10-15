package controller

import (
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func engineSchedule(snapshot store.RunSnapshot) (controllerwire.ScheduleSnapshot, error) {
	projection, err := snapshot.Scheduling()
	if err != nil {
		return controllerwire.ScheduleSnapshot{}, err
	}
	result := controllerwire.ScheduleSnapshot{RunID: snapshot.Run.ID, StoreNow: snapshot.StoreNow, CancelRequested: projection.CancelRequested, GuardRefusal: projection.GuardRefusal, Jobs: []controllerwire.ScheduleJob{}, Generators: []controllerwire.ScheduleGenerator{}, Expansions: []controllerwire.AcceptedExpansion{}}
	rows := make(map[string]store.NodeRow, len(snapshot.Nodes))
	for _, row := range snapshot.Nodes {
		rows[row.Ref.NodeID] = row
	}
	for _, node := range projection.Nodes {
		row := rows[node.ID]
		var job nodeproto.JobSpec
		if err := json.Unmarshal(row.Spec, &job); err != nil {
			return controllerwire.ScheduleSnapshot{}, err
		}
		result.Jobs = append(result.Jobs, controllerwire.ScheduleJob{JobDetail: engineJob(row), Declaration: job, Ready: node.Ready, Claimed: node.Claimed})
	}
	for _, generator := range projection.Expansions {
		result.Generators = append(result.Generators, controllerwire.ScheduleGenerator{ID: generator.GeneratorID, SourceJob: generator.SourceNodeID, State: generator.State, Members: generator.Members})
	}
	for _, expansion := range snapshot.Expansions {
		result.Expansions = append(result.Expansions, controllerwire.AcceptedExpansion{SourceJob: expansion.Source.NodeID, SourceGeneration: expansion.SourceGeneration, Body: expansion.Body, BodyHash: expansion.BodyHash})
	}
	return result, nil
}

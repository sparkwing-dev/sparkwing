package sessionledger

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// terminateContainer force-removes a container the step started. A container
// that is already gone is not an error: the point is that it is not running,
// and `docker rm -f` reports the same "no such container" whether it exited
// or never existed. Docker missing from PATH is reported, since a host that
// ran the container should still have the client.
func terminateContainer(ctx context.Context, name string) error {
	if name == "" {
		return errors.New("sessionledger: docker handle carries no container name")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("sessionledger: docker not on PATH to remove %s: %w", name, err)
	}
	out, err := exec.CommandContext(ctx, "docker", "rm", "-f", name).CombinedOutput()
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(string(out)), "no such container") {
		return nil
	}
	return fmt.Errorf("docker rm -f %s: %w: %s", name, err, strings.TrimSpace(string(out)))
}

// ContainersForRun lists the ids of containers labelled with the given run,
// running or stopped. Empty when docker is absent or answers nothing, so a
// caller can treat "no docker" and "no containers" the same.
func ContainersForRun(ctx context.Context, runID string) ([]string, error) {
	if runID == "" {
		return nil, nil
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, nil
	}
	out, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label=sparkwing.run="+runID).Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps for run %s: %w", runID, err)
	}
	var ids []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if id := strings.TrimSpace(line); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// RemoveContainersForRun force-removes every container labelled with the run.
// It is the net for a container a step started outside docker.Run (a
// hand-rolled `docker run --label sparkwing.run=$SPARKWING_RUN_ID`), which has
// no ledger record. It returns the ids it removed. A run whose containers are
// already gone removes nothing and is not an error.
func RemoveContainersForRun(ctx context.Context, runID string) ([]string, error) {
	ids, err := ContainersForRun(ctx, runID)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	var removed []string
	for _, id := range ids {
		if terr := terminateContainer(ctx, id); terr != nil {
			return removed, terr
		}
		removed = append(removed, id)
	}
	return removed, nil
}

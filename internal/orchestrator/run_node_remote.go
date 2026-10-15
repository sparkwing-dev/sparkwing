package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: a host stopped with no cause of its own, as a runner shutting down
// is, leaves its node and run rows to their claims' leases, so the controller
// hands them to another runner rather than record them cancelled or failed; a
// run's cancel, a lost claim or a timeout carries a cause and is recorded.
type stoppingHostState struct {
	*client.Client
	shuttingDown func() bool
}

// safety: no cause of its own is how a runner shutting down stops a host; every other stop names its reason.
func shuttingDownOn(ctx context.Context) func() bool {
	return func() bool { return errors.Is(context.Cause(ctx), context.Canceled) }
}

func (s stoppingHostState) FinishRun(ctx context.Context, runID, status, errMsg string) error {
	if s.shuttingDown() {
		return nil
	}
	return s.Client.FinishRun(ctx, runID, status, errMsg)
}

func (s stoppingHostState) FinishNodeWithReason(ctx context.Context, runID, nodeID, outcome, errMsg string, output []byte, reason string, exitCode *int) error {
	if s.shuttingDown() {
		return nil
	}
	return s.Client.FinishNodeWithReason(ctx, runID, nodeID, outcome, errMsg, output, reason, exitCode)
}

// safety: the node runs under the claim the dispatcher or this runner took,
// whose fence ctx carries, so every row and log write the host makes is
// checked against it and the node's terminal row is the result the
// dispatcher reads.
func hostDispatchedNode(ctx context.Context, p podNode) (runner.Result, error) {
	p.state = stoppingHostState{
		Client:       client.NewWithToken(p.controllerURL, nil, p.token),
		shuttingDown: shuttingDownOn(ctx),
	}
	res, err := hostPodNode(ctx, p)
	if err != nil {
		return runner.Result{}, err
	}
	return runner.Result{Outcome: res.Outcome, Err: res.Err}, nil
}

// ErrRepoNotAllowed marks a run whose repository this machine's owner did not
// allow it to build.
var ErrRepoNotAllowed = errors.New("repository not allowed on this machine")

// AdmitTriggerSource refuses trigger unless allow admits every repository it
// names: the remote fetchURL this machine would fetch, after any ssh-to-https
// rewrite, and the repository its GitHub fields name. A runner that fetches
// with its own credentials runs what it fetches as its own user, so the machine
// owner's list, not the controller, decides what it builds. A trigger naming
// no repository is refused, and so is a working-tree snapshot unless allow
// accepts them: the controller supplies a snapshot's code, which no
// repository on the list holds.
func AdmitTriggerSource(allow sourceurl.RepoAllowlist, trigger *store.Trigger, fetchURL string) error {
	named, err := sourceurl.TriggerRepository(trigger.RepoURL, trigger.TriggerEnv["GITHUB_REPOSITORY"],
		trigger.GithubOwner, trigger.GithubRepo)
	if err != nil {
		return err
	}
	if named == "" && fetchURL == "" {
		return fmt.Errorf("%w: run %s names no repository, so this machine's --allow-repo list (%s) cannot admit it",
			ErrRepoNotAllowed, trigger.ID, allow)
	}
	identities := []string{named}
	if fetchURL != "" {
		fetched, err := sourceurl.Identity(fetchURL)
		if err != nil {
			return err
		}
		identities = append(identities, fetched)
	}
	for _, id := range identities {
		if id != "" && !allow.Admits(id) {
			return fmt.Errorf("%w: run %s builds %s, which this machine's --allow-repo list (%s) does not name; "+
				"trigger it again for a runner whose list names it", ErrRepoNotAllowed, trigger.ID, id, allow)
		}
	}
	if strings.HasPrefix(trigger.TriggerSource, "pipeline-working-tree@") && !allow.AcceptsWorkingTree() {
		return fmt.Errorf("%w: run %s builds a working-tree snapshot whose code the controller supplied, which "+
			"this machine's --allow-repo list cannot vouch for; set accept_working_tree: true in the agent "+
			"section (or pass --accept-working-tree) to build snapshots of the listed repositories",
			ErrRepoNotAllowed, trigger.ID)
	}
	return nil
}

// TriggerSourceURL is the remote a runner fetches for a trigger's source.
func TriggerSourceURL(trigger *store.Trigger) (string, error) {
	if trigger == nil {
		return "", nil
	}
	return bincache.TriggerRepoURL(trigger.RepoURL, trigger.TriggerEnv["GITHUB_REPOSITORY"],
		trigger.GithubOwner, trigger.GithubRepo)
}

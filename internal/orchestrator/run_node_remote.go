package orchestrator

import (
	"errors"
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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

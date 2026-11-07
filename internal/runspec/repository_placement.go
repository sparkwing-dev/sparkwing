package runspec

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// Repository placement targets. A team or named tier claims through a shared
// team runner, cloud through Sparkwing Cloud capacity and github-actions
// through a GitHub Actions runner.
const (
	PlacementTeam          = "team"
	PlacementCloud         = "cloud"
	PlacementGitHubActions = "github-actions"
	placementNamePrefix    = "name="
)

// RepositoryTier is one ordered tier of a repository placement list. Hold is
// how long the tier keeps later tiers closed while one of its claimants is
// live and admissible. Labels narrow only this tier. Workflow and Runner name
// the GitHub Actions fallback.
type RepositoryTier struct {
	Target   string        `json:"target"`
	Labels   []string      `json:"labels,omitempty"`
	Hold     time.Duration `json:"hold"`
	Workflow string        `json:"workflow,omitempty"`
	Runner   string        `json:"runner,omitempty"`
}

// RepositoryJobPlacement replaces the pipeline list for one job. An empty
// Prefer restores the routing the job would have without repository placement.
type RepositoryJobPlacement struct {
	Job    string           `json:"job"`
	Prefer []RepositoryTier `json:"prefer"`
}

// RepositoryPlacement is the placement policy a repository's verified
// default-branch configuration declares for one pipeline. Revision is the
// default-branch commit the policy was read at, sealed next to the event's own
// execution revision so a branch or pull request cannot change it.
type RepositoryPlacement struct {
	Revision string                   `json:"revision"`
	Prefer   []RepositoryTier         `json:"prefer"`
	Jobs     []RepositoryJobPlacement `json:"jobs,omitempty"`
}

// ForJob returns the tiers that apply to a job: its own list when the policy
// names it, otherwise the pipeline list. An empty result means the job keeps
// the routing it would have without repository placement.
func (p *RepositoryPlacement) ForJob(job string) []RepositoryTier {
	if p == nil {
		return nil
	}
	for _, override := range p.Jobs {
		if override.Job == job {
			return override.Prefer
		}
	}
	return p.Prefer
}

// Validate refuses a policy the claim engine cannot interpret.
func (p RepositoryPlacement) Validate() error {
	if !IsCommitID(p.Revision) {
		return errors.New("repository placement requires its default-branch revision")
	}
	if err := ValidateRepositoryTiers(p.Prefer); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, job := range p.Jobs {
		if job.Job == "" || seen[job.Job] {
			return errors.New("repository placement job overrides need distinct job IDs")
		}
		seen[job.Job] = true
		if err := ValidateRepositoryTiers(job.Prefer); err != nil {
			return fmt.Errorf("job %s: %w", job.Job, err)
		}
	}
	return nil
}

// ValidateRepositoryTiers checks one ordered list: known targets, labels only
// on team runner tiers, nonnegative holds whose sum fits a duration, no
// repeated tier, and GitHub Actions only as the last tier.
func ValidateRepositoryTiers(tiers []RepositoryTier) error {
	var total time.Duration
	for i, tier := range tiers {
		if err := validateRepositoryTarget(tier.Target); err != nil {
			return err
		}
		actions := tier.Target == PlacementGitHubActions
		if actions != (tier.Workflow != "") || actions != (tier.Runner != "") {
			return errors.New("github-actions tiers, and only they, name a workflow and a runner image")
		}
		if actions && i != len(tiers)-1 {
			return errors.New("github-actions must be the last placement tier")
		}
		if len(tier.Labels) > 0 && !TeamRunnerTarget(tier.Target) {
			return fmt.Errorf("%s tiers take no labels; labels narrow team and name= tiers", tier.Target)
		}
		for _, term := range tier.Labels {
			if !validLabelTerm(term) {
				return fmt.Errorf("invalid placement label %q", term)
			}
		}
		if tier.Hold < 0 || tier.Hold > time.Duration(math.MaxInt64)-total {
			return errors.New("placement holds must be nonnegative and their sum must fit a duration")
		}
		total += tier.Hold
		for _, earlier := range tiers[:i] {
			if earlier.Target == tier.Target && slices.Equal(earlier.Labels, tier.Labels) {
				return fmt.Errorf("placement tier %s is listed twice", tier.Target)
			}
		}
	}
	return nil
}

// TeamRunnerTarget reports whether a target selects shared team runners.
func TeamRunnerTarget(target string) bool {
	return target == PlacementTeam || strings.HasPrefix(target, placementNamePrefix)
}

func validateRepositoryTarget(target string) error {
	switch target {
	case PlacementTeam, PlacementCloud, PlacementGitHubActions:
		return nil
	}
	if name, ok := strings.CutPrefix(target, placementNamePrefix); ok && name != "" && !strings.ContainsAny(name, ", \t\r\n=") {
		return nil
	}
	return fmt.Errorf("placement target %q must be team, name=<runner>, cloud or github-actions", target)
}

func validLabelTerm(term string) bool {
	if strings.TrimSpace(term) == "" || strings.ContainsAny(term, " \t\r\n") {
		return false
	}
	for _, alternative := range strings.Split(term, ",") {
		if alternative == "" {
			return false
		}
	}
	return true
}

// String renders a tier for previews and waiting-job notices, such as
// "team os=darwin, hold 5s" or "github-actions macos-latest".
func (t RepositoryTier) String() string {
	out := t.Target
	if len(t.Labels) > 0 {
		out += " " + strings.Join(t.Labels, " ")
	}
	if t.Runner != "" {
		out += " " + t.Runner
	}
	if t.Hold > 0 {
		out += ", hold " + t.Hold.String()
	}
	return out
}

// Active reports whether any job follows a tier list; a policy whose lists are
// all empty leaves every job on the routing it has without one.
func (p *RepositoryPlacement) Active() bool {
	if p == nil {
		return false
	}
	return len(p.Prefer) > 0 || slices.ContainsFunc(p.Jobs, func(job RepositoryJobPlacement) bool { return len(job.Prefer) > 0 })
}

// MaySatisfy reports whether some claimant of this tier could meet the job's
// operating-system requirements. Cloud executors run Linux, a GitHub-hosted
// image names its system, and a team tier is limited only by its own os=
// labels. It answers true whenever the system is unknown.
func (t RepositoryTier) MaySatisfy(requires []string) bool {
	var systems []string
	switch {
	case t.Target == PlacementCloud:
		systems = []string{"linux"}
	case t.Target == PlacementGitHubActions:
		for prefix, system := range map[string]string{"ubuntu-": "linux", "macos-": "darwin", "windows-": "windows"} {
			if strings.HasPrefix(t.Runner, prefix) {
				systems = []string{system}
			}
		}
	default:
		for _, term := range t.Labels {
			if allowed, ok := osTerm(term); ok {
				systems = allowed
			}
		}
	}
	if systems == nil {
		return true
	}
	for _, term := range requires {
		if allowed, ok := osTerm(term); ok && !slices.ContainsFunc(systems, func(system string) bool { return slices.Contains(allowed, system) }) {
			return false
		}
	}
	return true
}

// safety: only a term whose every alternative names an operating system constrains one; a mixed term may be met
// by another label.
func osTerm(term string) ([]string, bool) {
	var systems []string
	for _, alternative := range strings.Split(term, ",") {
		system, ok := strings.CutPrefix(strings.TrimSpace(alternative), "os=")
		if !ok || system == "" {
			return nil, false
		}
		systems = append(systems, system)
	}
	return systems, len(systems) > 0
}

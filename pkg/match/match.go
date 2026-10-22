// Package match evaluates runner label and repository requirements.
//
// A selector is a list of terms. Terms are ANDed; the comma-separated
// alternatives inside a term are ORed. Each alternative is a bare label
// ("gpu") or key=value ("arch=arm64"). The keys name, class, os, arch and
// location, and the alias local, are answered from the agent's granted or
// observed facts, never from labels the agent asserted for itself.
package match

import (
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

// Agent classes. A class is granted by the credential, never asserted.
const (
	ClassCoordinator = "coordinator"
	ClassAgent       = "agent"
	ClassCloud       = "cloud"
)

// Reason names why a verdict refuses.
type Reason string

// Reasons identify the eligibility rule that refused a demand.
const (
	ReasonReservation      Reason = "reservation"
	ReasonRepo             Reason = "repo"
	ReasonTrustedPlacement Reason = "trusted_placement"
	ReasonSelector         Reason = "selector"
)

// Resources is the physical allocation of a Cloud size. It is never used for matching.
type Resources struct {
	Cores       float64 `json:"cores" yaml:"cores"`
	MemoryBytes int64   `json:"memory_bytes" yaml:"memory_bytes"`
}

// RepoFilter decides which repositories an agent accepts. named reports
// whether the fields name a repository at all.
type RepoFilter interface {
	Patterns() []string
	AdmitsRepository(repoURL, githubRepository, githubOwner, githubRepo string) (named, admitted bool)
}

// ParseAccept parses an accept list of host/path repository patterns, where
// '*' matches within one path segment. An empty list admits nothing, and so
// does the filter returned with an error, so a caller fails closed.
func ParseAccept(patterns []string) (RepoFilter, error) {
	allow, err := sourceurl.ParseRepoAllowlist(patterns)
	if err != nil {
		return sourceurl.RepoAllowlist{}, err
	}
	return allow, nil
}

// Repository is the repository fields of the run a demand belongs to.
type Repository struct {
	URL, GitHubRepository, GitHubOwner, GitHubRepo string
	// Undecodable marks fields that could not be read, which no filter admits.
	Undecodable bool
}

// Profile is what one agent is and has.
type Profile struct {
	// ReservedPools comes only from authenticated granted reservations. A pool=
	// entry in Labels never grants membership, even if the caller omitted filtering.
	ReservedPools []string
	// Name, Class and Location are granted by the credential or the enrolled
	// row. Location is an enrolled executor's local or cloud placement.
	Name, Class, Location string
	// Labels are free-form. [SelfAsserted] strips the fixed keys from labels an
	// agent sends for itself.
	Labels []string
	// OS and Arch are observed. Empty falls back to an os= or arch= label, which
	// is how a runner that reports no platform has always matched.
	OS, Arch string
	// Accept narrows the repositories the agent takes; nil takes every one.
	Accept RepoFilter
}

// Demand is what one piece of work asks of an agent.
type Demand struct {
	// Selector is the sealed, resolved requires list, including inherited run requirements.
	Selector []string
	// AllowedPools is the intake snapshot of team-admin access, never caller input.
	AllowedPools []string
	// Prefers is the resolved row preference list. Preferences never relax eligibility.
	Prefers []string
	// Repo is the run's repository, or nil when the caller did not resolve it
	// because no filter could turn on it.
	Repo *Repository
	// Trigger demands a repository the filter admits; a node admits a run that
	// names none, since it then fetches no source.
	Trigger bool
}

// Verdict is the outcome of a check. The zero Verdict admits.
type Verdict struct {
	Reason Reason
	// PreferenceScore counts satisfied preferences only for an eligible profile.
	PreferenceScore int
	// Term is the selector term that failed, when Reason is a selector reason.
	Term string
}

// OK reports whether the verdict admits.
func (v Verdict) OK() bool { return v.Reason == "" }

func (v Verdict) String() string {
	if v.Term != "" {
		return string(v.Reason) + " (" + v.Term + ")"
	}
	return string(v.Reason)
}

func evaluateRequirements(p Profile, d Demand) Verdict {
	if p.Accept != nil && d.Repo != nil && !admitsRepo(p.Accept, *d.Repo, d.Trigger) {
		return Verdict{Reason: ReasonRepo}
	}
	for _, term := range d.Selector {
		if strings.TrimSpace(term) == "" || termSatisfied(p, term) {
			continue
		}
		if placementTerm(term) {
			return Verdict{Reason: ReasonTrustedPlacement, Term: term}
		}
		return Verdict{Reason: ReasonSelector, Term: term}
	}
	return Verdict{}
}

// TermSatisfied reports whether p satisfies one selector term. Soft
// preferences rank agents with it; a hard selector goes through [Evaluate].
func TermSatisfied(p Profile, term string) bool {
	return strings.TrimSpace(term) == "" || termSatisfied(p, term)
}

// SelfAsserted drops the labels an agent may not assert for itself: the fixed
// keys name, class, team and pool, and the placement vocabulary. os= and arch= stay,
// because they only steer scheduling.
func SelfAsserted(labels []string) []string {
	out := make([]string, 0, len(labels))
	for _, label := range labels {
		label = strings.TrimSpace(label)
		key, _, _ := strings.Cut(label, "=")
		switch {
		case label == "", label == "local", key == "location", key == "name", key == "class", key == "team", key == "pool":
			continue
		}
		out = append(out, label)
	}
	return out
}

func admitsRepo(filter RepoFilter, repo Repository, trigger bool) bool {
	if repo.Undecodable {
		return false
	}
	named, admitted := filter.AdmitsRepository(repo.URL, repo.GitHubRepository, repo.GitHubOwner, repo.GitHubRepo)
	if trigger {
		return named && admitted
	}
	return !named || admitted
}

func termSatisfied(p Profile, term string) bool {
	for _, alt := range strings.Split(term, ",") {
		if alt = strings.TrimSpace(alt); alt != "" && p.has(alt) {
			return true
		}
	}
	return false
}

func placementTerm(term string) bool {
	for _, alt := range strings.Split(term, ",") {
		alt = strings.TrimSpace(alt)
		if alt == "local" || strings.HasPrefix(alt, "location=") {
			return true
		}
	}
	return false
}

// safety: a label in a Profile is trusted by construction, because
// [SelfAsserted] strips the fixed keys from anything an agent sent for itself,
// so a granted fact that does not match still falls back to the labels.
func (p Profile) has(alt string) bool {
	key, value, _ := strings.Cut(alt, "=")
	switch {
	case key == "os" && p.OS != "":
		return value == p.OS
	case key == "arch" && p.Arch != "":
		return value == p.Arch
	case alt == "local", alt == "location=coordinator":
		if p.Class == ClassCoordinator {
			return true
		}
	case key == "location", key == "name", key == "class":
		granted := map[string]string{"location": p.Location, "name": p.Name, "class": p.Class}[key]
		if granted != "" && value == granted {
			return true
		}
	}
	return p.label(alt)
}

func (p Profile) label(label string) bool {
	for _, have := range p.Labels {
		if have == label {
			return true
		}
	}
	return false
}

// Package match decides whether an agent may run a piece of work. Every claim
// route asks [Evaluate] whether an agent could ever run a demand, and asks
// [Fits] whether it has room for it now, so the routes cannot disagree about
// who may take what.
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

// Reasons, in the order [Evaluate] and [Fits] check them.
const (
	ReasonRepo             Reason = "repo"
	ReasonTrustedPlacement Reason = "trusted_placement"
	ReasonSelector         Reason = "selector"
	ReasonShape            Reason = "shape"
	ReasonAvailability     Reason = "availability"
)

// Resources is a CPU and memory amount. A zero capacity dimension is unknown
// and admits any request in that dimension.
type Resources struct {
	Cores       float64 `json:"cores" yaml:"cores"`
	MemoryBytes int64   `json:"memory_bytes" yaml:"memory_bytes"`
}

// RepoFilter decides which repositories an agent accepts. named reports
// whether the fields name a repository at all.
type RepoFilter interface {
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
	// Name, Class and Location are granted by the credential or the enrolled
	// row. Location is an enrolled executor's local or cloud placement.
	Name, Class, Location string
	// Labels are free-form. [SelfAsserted] strips the fixed keys from labels an
	// agent sends for itself.
	Labels []string
	// OS and Arch are observed. Empty falls back to an os= or arch= label, which
	// is how a runner that reports no platform has always matched.
	OS, Arch string
	// Capacity is the most the agent could ever give one node.
	Capacity Resources
	// Accept narrows the repositories the agent takes; nil takes every one.
	Accept RepoFilter
}

// Demand is what one piece of work asks of an agent.
type Demand struct {
	Selector []string
	// Repo is the run's repository, or nil when the caller did not resolve it
	// because no filter could turn on it.
	Repo *Repository
	// Trigger demands a repository the filter admits; a node admits a run that
	// names none, since it then fetches no source.
	Trigger bool
	Request Resources
}

// Verdict is the outcome of a check. The zero Verdict admits.
type Verdict struct {
	Reason Reason
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

// Evaluate reports whether p could ever run d: the repository filter, then the
// selector, then the request against capacity. It reads no availability, so a
// busy agent is still eligible.
func Evaluate(p Profile, d Demand) Verdict {
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
	if !fits(p.Capacity, d.Request) {
		return Verdict{Reason: ReasonShape}
	}
	return Verdict{}
}

// Fits reports whether request fits in what the agent has free now. A
// dimension the agent reports no capacity for is unknown and admits any
// request, while a known capacity makes a zero availability mean none free.
func Fits(capacity, available, request Resources) Verdict {
	if (capacity.Cores > 0 && request.Cores > available.Cores) ||
		(capacity.MemoryBytes > 0 && request.MemoryBytes > available.MemoryBytes) {
		return Verdict{Reason: ReasonAvailability}
	}
	return Verdict{}
}

// TermSatisfied reports whether p satisfies one selector term. Soft
// preferences rank agents with it; a hard selector goes through [Evaluate].
func TermSatisfied(p Profile, term string) bool {
	return strings.TrimSpace(term) == "" || termSatisfied(p, term)
}

// SelfAsserted drops the labels an agent may not assert for itself: the fixed
// keys name, class and team, and the placement vocabulary. os= and arch= stay,
// because they only steer scheduling.
func SelfAsserted(labels []string) []string {
	out := make([]string, 0, len(labels))
	for _, label := range labels {
		label = strings.TrimSpace(label)
		key, _, _ := strings.Cut(label, "=")
		switch {
		case label == "", label == "local", key == "location", key == "name", key == "class", key == "team":
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

func fits(have, want Resources) bool {
	return (have.Cores <= 0 || want.Cores <= have.Cores) &&
		(have.MemoryBytes <= 0 || want.MemoryBytes <= have.MemoryBytes)
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

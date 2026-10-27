package runspec

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
)

// TrustClass names how the controller established the identity a run speaks for.
type TrustClass string

const (
	// TrustVerifiedDefault is a signed GitHub App push to the repository's default branch.
	TrustVerifiedDefault TrustClass = "verified-default"
	// TrustVerified is any other signed GitHub App delivery.
	TrustVerified TrustClass = "verified"
	// TrustActions is a run bound to a GitHub Actions push that GitHub's own ID token proved.
	TrustActions TrustClass = "actions"
	// TrustSchedule is an unmodified fire of a schedule a team admin armed on the
	// repository's default branch, whose commit GitHub resolved at fire time.
	TrustSchedule TrustClass = "schedule"
	// TrustAsserted is every run whose ref, commit and inputs a principal chose.
	TrustAsserted TrustClass = "asserted"
)

// TrustSet is one named rule over trust classes. The store renders every SQL trust
// list from these sets, so changing a rule edits one line.
type TrustSet []TrustClass

// Has reports whether the set admits the class.
func (s TrustSet) Has(c TrustClass) bool { return slices.Contains(s, c) }

var (
	// DefaultBranchWorkTrust is verified default-branch work: a signed push to the
	// default branch, or an unmodified fire of an admin-armed default-branch schedule
	// (decisions 31, 34, 60). Any other class on the default branch is ordinary work.
	DefaultBranchWorkTrust = TrustSet{TrustVerifiedDefault, TrustSchedule}
	// AppRepositoryTrust proved its repository through the GitHub App: a signed
	// delivery or a schedule whose commit the App resolved. Pool access rules grant it.
	AppRepositoryTrust = TrustSet{TrustVerified, TrustVerifiedDefault, TrustSchedule}
	// AppDeliveryTrust is a signed GitHub App delivery on any branch.
	AppDeliveryTrust = TrustSet{TrustVerified, TrustVerifiedDefault}
	// GitHubEventTrust carries a ref and commit GitHub proved for one event, from an
	// App delivery or an Actions push, which an Actions push binding can match.
	GitHubEventTrust = TrustSet{TrustActions, TrustVerified, TrustVerifiedDefault}
	// AllTrust lists every class.
	AllTrust = TrustSet{TrustVerifiedDefault, TrustVerified, TrustActions, TrustSchedule, TrustAsserted}
)

// appEvents are the signed delivery events that may seal verified provenance.
var appEvents = []string{"push", "pull_request", "release", "create", "delete"}

// Provenance is the sealed identity a run speaks for: the repository, ref, commit,
// event and pipeline that OIDC subjects, data scopes, pool rules and secret release
// read. Its fields are unexported so that only the intake constructors below, which
// the store calls from verified facts, can produce one. A child or a retry copies its
// parent's value whole or is sealed again as asserted.
type Provenance struct {
	identity provenanceJSON
}

// AppProvenance seals the identity of a signed, replay-checked GitHub App delivery.
// A pull request delivery also seals the base branch its runs may read caches from.
func AppProvenance(app AppFacts, pr Optional[PullRequest], pipeline string) (Provenance, error) {
	p := Provenance{identity: provenanceJSON{Trust: TrustVerified, RepositoryID: app.RepositoryID, Repository: app.Repository, Ref: app.Ref, SHA: strings.ToLower(app.SHA), Event: app.Event, Pipeline: pipeline, DefaultBranch: app.DefaultBranch, Delivery: app.DeliveryKey}}
	if app.VerifiedAt.IsZero() {
		return Provenance{}, errors.New("provenance requires a verified App delivery")
	}
	if app.Event == "pull_request" && pr.Set && pr.Value.BaseRef != "" {
		p.identity.Base = NormalizedBranch(pr.Value.BaseRef)
	}
	if app.DefaultBranchPush && app.Event == "push" && app.DefaultBranch != "" && app.Ref == "refs/heads/"+app.DefaultBranch {
		p.identity.Trust = TrustVerifiedDefault
	}
	return p, p.validate()
}

// ScheduleProvenance seals an unmodified fire of a schedule a team admin armed on
// the repository's default branch. The commit is that branch's head, which GitHub
// resolved at fire time, so the run is as trusted as a push to the default branch
// for pools and cache reads, while it still writes only its own scope.
func ScheduleProvenance(schedule string, repositoryID int64, repository, defaultBranch, sha, pipeline string) (Provenance, error) {
	branch := NormalizedBranch(defaultBranch)
	p := Provenance{identity: provenanceJSON{Trust: TrustSchedule, RepositoryID: repositoryID, Repository: repository, Ref: "refs/heads/" + branch, SHA: strings.ToLower(sha), Pipeline: pipeline, DefaultBranch: branch, Writer: "schedule/" + escapeWriter(schedule)}}
	if schedule == "" || NormalizedBranch(branch) == "" {
		return Provenance{}, errors.New("schedule provenance requires the schedule and the branch it follows")
	}
	return p, p.validate()
}

// ActionsProvenance seals a run whose credential is bound to one GitHub Actions push
// of one repository.
func ActionsProvenance(repositoryID int64, repository, ref, sha, pipeline string) (Provenance, error) {
	p := Provenance{identity: provenanceJSON{Trust: TrustActions, RepositoryID: repositoryID, Repository: repository, Ref: ref, SHA: strings.ToLower(sha), Pipeline: pipeline, Writer: "actions"}}
	return p, p.validate()
}

// AssertedProvenance seals a run whose identity a principal chose. Writer keys its
// data scope, so one writer's records never reach another writer's runs.
func AssertedProvenance(writer Writer, ref, sha, pipeline string) (Provenance, error) {
	p := Provenance{identity: provenanceJSON{Trust: TrustAsserted, Ref: ref, SHA: strings.ToLower(sha), Pipeline: pipeline, Writer: string(writer)}}
	return p, p.validate()
}

// AssertedChildProvenance seals a child that left its parent's revision. No
// pipeline vouched for its code, so it reads only team-wide shared secrets.
func AssertedChildProvenance(writer Writer, ref, sha, pipeline string) (Provenance, error) {
	p := Provenance{identity: provenanceJSON{Trust: TrustAsserted, Ref: ref, SHA: strings.ToLower(sha), Pipeline: pipeline, Writer: string(writer), SharedOnly: true}}
	return p, p.validate()
}

// SharedSecretsOnly reports a run that may read only team-wide shared secrets.
func (p Provenance) SharedSecretsOnly() bool { return p.identity.SharedOnly }

// FetchedFrom records that the controller itself fetched an asserted run's code
// from this GitHub repository through the team's App installation. It proves
// the repository for pipeline secrets and the OIDC repository claims, and
// repository, the owner/name GitHub reported, may be empty when no lookup
// reported one. ref and sha are the branch or tag head the App resolved and
// the run's code ran at, or both empty when the code ran elsewhere (decision
// 72); they are claimed in OIDC only. The trust class, pools and data scopes
// stay asserted.
func (p Provenance) FetchedFrom(repositoryID int64, repository, ref, sha string) (Provenance, error) {
	if p.identity.Trust != TrustAsserted || p.identity.SharedOnly || repositoryID <= 0 {
		return Provenance{}, errors.New("only an asserted run the controller fetched names a fetched repository")
	}
	p.identity.Fetched, p.identity.Repository, p.identity.FetchedRef, p.identity.FetchedSHA = repositoryID, repository, ref, strings.ToLower(sha)
	return p, p.validate()
}

// SecretRepositoryID is the proven GitHub repository whose pipeline secrets the
// run reads: the repository a verified run sealed, or the one the controller
// fetched an asserted run's code from. Zero means shared secrets only.
func (p Provenance) SecretRepositoryID() int64 {
	if p.identity.RepositoryID > 0 {
		return p.identity.RepositoryID
	}
	return p.identity.Fetched
}

// ReadingDefault adds the repository's verified default branch, which intake learned
// from verified records, as a cache read fallback for an unverified run. It never
// changes the scope the run writes or the identity it claims.
func (p Provenance) ReadingDefault(branch string) (Provenance, error) {
	if p.Verified() {
		return Provenance{}, errors.New("a verified run carries its own default branch")
	}
	p.identity.DefaultBranch = NormalizedBranch(branch)
	return p, p.validate()
}

// Writer identifies whose asserted records share a data scope.
type Writer string

// PrincipalWriter keys asserted data by the principal that submitted the run.
func PrincipalWriter(kind, id string) Writer {
	return Writer(escapeWriter(kind) + "/" + escapeWriter(id))
}

// Principal returns the submitting principal of an asserted writer, or an empty actor
// for writers scoped to a run or automation.
func (w Writer) Principal() Actor {
	kind, id, ok := strings.Cut(string(w), "/")
	if !ok || kind != "account" && kind != "machine" && kind != "local" {
		return Actor{}
	}
	return Actor{Kind: kind, ID: strings.NewReplacer("%2F", "/", "%3A", ":", "%25", "%").Replace(id)}
}

// RunWriter keys asserted data by one run's lineage when no principal chose it.
func RunWriter(run string) Writer { return Writer("run/" + escapeWriter(run)) }

func escapeWriter(s string) string {
	return strings.NewReplacer("%", "%25", ":", "%3A", "/", "%2F").Replace(s)
}

func (p Provenance) validate() error {
	if p.identity.Pipeline == "" {
		return errors.New("provenance requires a pipeline")
	}
	if p.identity.Fetched < 0 || p.identity.Fetched > 0 && (p.identity.Trust != TrustAsserted || p.identity.SharedOnly) {
		return errors.New("only an asserted run the controller fetched names a fetched repository")
	}
	if (p.identity.FetchedRef == "") != (p.identity.FetchedSHA == "") || p.identity.FetchedRef != "" && (p.identity.Fetched == 0 || !strings.HasPrefix(p.identity.FetchedRef, "refs/heads/") && !strings.HasPrefix(p.identity.FetchedRef, "refs/tags/") || !IsCommitID(p.identity.FetchedSHA)) {
		return errors.New("a fetched run names the branch or tag head it ran together with its commit")
	}
	if p.identity.SHA != "" && !IsCommitID(p.identity.SHA) {
		return errors.New("provenance commit must be a full commit ID")
	}
	if p.identity.Repository == "" && p.identity.Trust != TrustAsserted || p.identity.Repository != "" && p.identity.Trust == TrustAsserted && p.identity.Fetched == 0 {
		return errors.New("a GitHub-proven run seals its repository id and name together, and an asserted run names only a repository the controller fetched")
	}
	switch p.identity.Trust {
	case TrustVerified, TrustVerifiedDefault:
		if p.identity.RepositoryID <= 0 || p.identity.Delivery == "" || p.identity.Ref == "" || p.identity.SHA == "" || p.identity.Writer != "" || !slices.Contains(appEvents, p.identity.Event) {
			return errors.New("verified provenance requires a repository, delivery, event, ref and commit")
		}
		if p.identity.SharedOnly {
			return errors.New("only an asserted child is limited to shared secrets")
		}
		if p.identity.Base != "" && p.identity.Event != "pull_request" {
			return errors.New("only a pull request seals a base branch")
		}
		if p.identity.Trust == TrustVerifiedDefault && (p.identity.Event != "push" || p.identity.DefaultBranch == "" || p.identity.Ref != "refs/heads/"+p.identity.DefaultBranch) {
			return errors.New("verified-default provenance is a push to the default branch")
		}
	case TrustActions, TrustSchedule, TrustAsserted:
		if p.identity.Delivery != "" || p.identity.Event != "" || p.identity.Base != "" || p.identity.Writer == "" || strings.Contains(p.identity.Writer, ":") {
			return errors.New("unverified provenance carries only a writer, ref and commit")
		}
		if (p.identity.Trust == TrustAsserted) != (p.identity.RepositoryID == 0) || p.identity.RepositoryID < 0 || p.identity.SharedOnly && p.identity.Trust != TrustAsserted {
			return errors.New("only a run bound to a GitHub repository names its repository")
		}
		if p.identity.Trust != TrustAsserted && (p.identity.Ref == "" || p.identity.SHA == "") {
			return errors.New("actions and schedule provenance require the proven ref and commit")
		}
		if p.identity.Trust == TrustSchedule && (p.identity.DefaultBranch == "" || p.identity.Ref != "refs/heads/"+p.identity.DefaultBranch) {
			return errors.New("schedule provenance follows the default branch")
		}
	default:
		return fmt.Errorf("unknown trust class %q", p.identity.Trust)
	}
	return nil
}

// Valid reports whether the value has a shape intake can seal; the zero value
// speaks for nothing.
func (p Provenance) Valid() bool { return p.validate() == nil }

// Trust returns the class that established this identity.
func (p Provenance) Trust() TrustClass { return p.identity.Trust }

// Verified reports a signed GitHub App delivery, on any branch.
func (p Provenance) Verified() bool { return AppDeliveryTrust.Has(p.identity.Trust) }

// DefaultBranchWork reports verified default-branch work. Reserved pools, fresh-pod
// placement, context sharing and the default-branch cache all read this one rule,
// and the store's SQL renders the same test. Seal validation already pins both
// classes to the default branch; the ref check keeps a stored seal honest.
func (p Provenance) DefaultBranchWork() bool {
	return DefaultBranchWorkTrust.Has(p.identity.Trust) && p.identity.DefaultBranch != "" && p.identity.Ref == "refs/heads/"+p.identity.DefaultBranch
}

// Repository is the owner/name GitHub reported for RepositoryID when intake sealed
// the run, or for the repository the controller fetched an asserted run's code from,
// and empty for any other run a principal chose.
func (p Provenance) Repository() string { return p.identity.Repository }

// RepositoryID is the GitHub repository a signed delivery, an Actions push binding
// or App automation consent established, and zero for a run a principal chose.
func (p Provenance) RepositoryID() int64 { return p.identity.RepositoryID }

// Ref is the ref the run speaks for. GitHub establishes it for verified, actions and
// schedule runs; an asserted ref is the submitter's text.
func (p Provenance) Ref() string { return p.identity.Ref }

// SHA is the execution commit the run speaks for.
func (p Provenance) SHA() string { return p.identity.SHA }

// Pipeline is the pipeline the run speaks for. A child at its parent's exact revision
// speaks for its parent's pipeline; every other run speaks for its own.
func (p Provenance) Pipeline() string { return p.identity.Pipeline }

// DefaultBranch is the repository's default branch: from the delivery for a verified
// run, from the GitHub App's fire-time resolution for an admin-armed schedule, and
// from verified records for any other run, which reads its cache from there.
func (p Provenance) DefaultBranch() string { return p.identity.DefaultBranch }

// PullRequestBase is the base branch name of a verified pull request delivery.
func (p Provenance) PullRequestBase() string { return p.identity.Base }

// Delivery is the verified delivery digest, empty for every unverified class.
func (p Provenance) Delivery() string { return p.identity.Delivery }

// Writer keys the data scope of every unverified class, and is empty when verified.
func (p Provenance) Writer() Writer { return Writer(p.identity.Writer) }

// OIDC returns the trigger, repository ID, ref and commit an ID token may claim.
// An asserted run proves neither its ref nor its commit, so it claims neither,
// unless the controller fetched its code from a repository: it then claims that
// repository, and the branch or tag head it ran when the App resolved one.
func (p Provenance) OIDC() (trigger string, repositoryID int64, ref, sha string) {
	switch p.identity.Trust {
	case TrustVerified, TrustVerifiedDefault:
		return p.identity.Event, p.identity.RepositoryID, p.identity.Ref, p.identity.SHA
	case TrustSchedule:
		return "cron", p.identity.RepositoryID, p.identity.Ref, p.identity.SHA
	case TrustActions:
		return "actions", p.identity.RepositoryID, p.identity.Ref, p.identity.SHA
	}
	return "manual", p.identity.Fetched, p.identity.FetchedRef, p.identity.FetchedSHA
}

type provenanceJSON struct {
	Trust         TrustClass `json:"trust"`
	RepositoryID  int64      `json:"repository_id,omitempty"`
	Repository    string     `json:"repository,omitempty"`
	Ref           string     `json:"ref,omitempty"`
	SHA           string     `json:"sha,omitempty"`
	Event         string     `json:"event,omitempty"`
	Pipeline      string     `json:"pipeline,omitempty"`
	DefaultBranch string     `json:"default_branch,omitempty"`
	Base          string     `json:"pull_request_base,omitempty"`
	Delivery      string     `json:"delivery,omitempty"`
	Writer        string     `json:"writer,omitempty"`
	SharedOnly    bool       `json:"shared_secrets_only,omitempty"`
	Fetched       int64      `json:"fetched_repository_id,omitempty"`
	FetchedRef    string     `json:"fetched_ref,omitempty"`
	FetchedSHA    string     `json:"fetched_sha,omitempty"`
}

// MarshalJSON persists the sealed fields inside the request seal.
func (p Provenance) MarshalJSON() ([]byte, error) {
	return json.Marshal(p.identity)
}

// UnmarshalJSON restores a stored seal and refuses a value whose shape no constructor produces.
func (p *Provenance) UnmarshalJSON(raw []byte) error {
	var v provenanceJSON
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	decoded := Provenance{identity: v}
	if decoded != (Provenance{}) {
		if err := decoded.validate(); err != nil {
			return err
		}
	}
	*p = decoded
	return nil
}

// BranchSelector matches branch names with path.Match globs. Patterns and branches
// compare without a refs/heads/ prefix, so "main" and "refs/heads/main" select the
// same branch. An empty selector matches every branch.
type BranchSelector []string

// NormalizedBranch strips the refs/heads/ prefix every branch comparison drops.
func NormalizedBranch(branch string) string {
	return strings.TrimPrefix(strings.TrimSpace(branch), "refs/heads/")
}

// ParseBranchSelector normalizes patterns and refuses a malformed glob.
func ParseBranchSelector(patterns []string) (BranchSelector, error) {
	out := make(BranchSelector, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = NormalizedBranch(pattern)
		if pattern == "" {
			return nil, errors.New("branch pattern is empty")
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return nil, fmt.Errorf("branch pattern %q: %w", pattern, err)
		}
		out = append(out, pattern)
	}
	return out, nil
}

// Matches reports whether branch, with or without refs/heads/, matches a pattern.
func (s BranchSelector) Matches(branch string) bool {
	if len(s) == 0 {
		return true
	}
	branch = NormalizedBranch(branch)
	for _, pattern := range s {
		if matched, err := path.Match(NormalizedBranch(pattern), branch); err == nil && matched {
			return true
		}
	}
	return false
}

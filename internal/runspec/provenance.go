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
	// TrustSchedule is an unmodified fire of a schedule on this controller.
	TrustSchedule TrustClass = "schedule"
	// TrustAsserted is every run whose ref, commit and inputs a principal chose.
	TrustAsserted TrustClass = "asserted"
)

// appEvents are the signed delivery events that may seal verified provenance.
var appEvents = []string{"push", "pull_request", "release", "create", "delete"}

// Provenance is the sealed identity a run speaks for: the repository, ref, commit,
// event and pipeline that OIDC subjects, data scopes, pool rules and secret release
// read. Its fields are unexported so that only the intake constructors below, which
// the store calls from verified facts, can produce one. A child or a retry copies its
// parent's value whole or is sealed again as asserted.
type Provenance struct {
	trust         TrustClass
	repositoryID  int64
	ref           string
	sha           string
	event         string
	pipeline      string
	defaultBranch string
	base          string
	delivery      string
	writer        string
}

// AppProvenance seals the identity of a signed, replay-checked GitHub App delivery.
// A pull request delivery also seals the base branch its runs may read caches from.
func AppProvenance(app AppFacts, pr Optional[PullRequest], pipeline string) (Provenance, error) {
	p := Provenance{trust: TrustVerified, repositoryID: app.RepositoryID, ref: app.Ref, sha: strings.ToLower(app.SHA), event: app.Event, pipeline: pipeline, defaultBranch: app.DefaultBranch, delivery: app.DeliveryKey}
	if app.VerifiedAt.IsZero() {
		return Provenance{}, errors.New("provenance requires a verified App delivery")
	}
	if app.Event == "pull_request" && pr.Set && pr.Value.BaseRef != "" {
		p.base = NormalizeBranch(pr.Value.BaseRef)
	}
	if app.DefaultBranchPush && app.Event == "push" && app.DefaultBranch != "" && app.Ref == "refs/heads/"+app.DefaultBranch {
		p.trust = TrustVerifiedDefault
	}
	return p, p.validate()
}

// ScheduleProvenance seals an unmodified schedule fire. Each schedule and the
// principal that armed it write their own data scope.
func ScheduleProvenance(schedule, armedBy, ref, sha, pipeline string) (Provenance, error) {
	p := Provenance{trust: TrustSchedule, ref: ref, sha: strings.ToLower(sha), pipeline: pipeline, writer: "schedule/" + escapeWriter(schedule) + "/" + escapeWriter(armedBy)}
	if schedule == "" || armedBy == "" {
		return Provenance{}, errors.New("schedule provenance requires the schedule and its armer")
	}
	return p, p.validate()
}

// ActionsProvenance seals a run whose credential is bound to one GitHub Actions push.
func ActionsProvenance(ref, sha, pipeline string) (Provenance, error) {
	p := Provenance{trust: TrustActions, ref: ref, sha: strings.ToLower(sha), pipeline: pipeline, writer: "actions"}
	return p, p.validate()
}

// AssertedProvenance seals a run whose identity a principal chose. Writer keys its
// data scope, so one writer's records never reach another writer's runs.
func AssertedProvenance(writer Writer, ref, sha, pipeline string) (Provenance, error) {
	p := Provenance{trust: TrustAsserted, ref: ref, sha: strings.ToLower(sha), pipeline: pipeline, writer: string(writer)}
	return p, p.validate()
}

// Writer identifies whose asserted records share a data scope.
type Writer string

// PrincipalWriter keys asserted data by the principal that submitted the run.
func PrincipalWriter(kind, id string) Writer {
	return Writer(escapeWriter(kind) + "/" + escapeWriter(id))
}

// RunWriter keys asserted data by one run's lineage when no principal chose it.
func RunWriter(run string) Writer { return Writer("run/" + escapeWriter(run)) }

func escapeWriter(s string) string {
	return strings.NewReplacer("%", "%25", ":", "%3A", "/", "%2F").Replace(s)
}

func (p Provenance) validate() error {
	if p.pipeline == "" {
		return errors.New("provenance requires a pipeline")
	}
	if p.sha != "" && !IsCommitID(p.sha) {
		return errors.New("provenance commit must be a full commit ID")
	}
	switch p.trust {
	case TrustVerified, TrustVerifiedDefault:
		if p.repositoryID <= 0 || p.delivery == "" || p.ref == "" || p.sha == "" || p.writer != "" || !slices.Contains(appEvents, p.event) {
			return errors.New("verified provenance requires a repository, delivery, event, ref and commit")
		}
	case TrustActions, TrustSchedule, TrustAsserted:
		if p.repositoryID != 0 || p.delivery != "" || p.event != "" || p.defaultBranch != "" || p.base != "" || p.writer == "" || strings.Contains(p.writer, ":") {
			return errors.New("unverified provenance carries only a writer, ref and commit")
		}
		if p.trust == TrustActions && (p.ref == "" || p.sha == "") {
			return errors.New("actions provenance requires the bound ref and commit")
		}
	default:
		return fmt.Errorf("unknown trust class %q", p.trust)
	}
	return nil
}

// Valid reports whether intake sealed this value; the zero value speaks for nothing.
func (p Provenance) Valid() bool { return p.validate() == nil }

// Trust returns the class that established this identity.
func (p Provenance) Trust() TrustClass { return p.trust }

// Verified reports a signed GitHub App delivery, on any branch.
func (p Provenance) Verified() bool {
	return p.trust == TrustVerified || p.trust == TrustVerifiedDefault
}

// RepositoryID is GitHub's repository ID for verified provenance and zero otherwise.
func (p Provenance) RepositoryID() int64 { return p.repositoryID }

// Ref is the ref the run speaks for. Only verified, actions and schedule refs are
// established by the controller; an asserted ref is the submitter's text.
func (p Provenance) Ref() string { return p.ref }

// SHA is the execution commit the run speaks for.
func (p Provenance) SHA() string { return p.sha }

// Pipeline is the pipeline the run speaks for. A child speaks for its parent's pipeline.
func (p Provenance) Pipeline() string { return p.pipeline }

// Event is the verified GitHub event, empty for every unverified class.
func (p Provenance) Event() string { return p.event }

// DefaultBranch is the repository's default branch name from a verified delivery.
func (p Provenance) DefaultBranch() string { return p.defaultBranch }

// PullRequestBase is the base branch name of a verified pull request delivery.
func (p Provenance) PullRequestBase() string { return p.base }

// Delivery is the verified delivery digest, empty for every unverified class.
func (p Provenance) Delivery() string { return p.delivery }

// Writer keys the data scope of every unverified class, and is empty when verified.
func (p Provenance) Writer() Writer { return Writer(p.writer) }

// OIDC returns the trigger, repository ID, ref and commit an ID token may claim.
// An asserted run proves none of its ref, commit or repository, so it claims none.
func (p Provenance) OIDC() (trigger string, repositoryID int64, ref, sha string) {
	switch p.trust {
	case TrustVerified, TrustVerifiedDefault:
		return p.event, p.repositoryID, p.ref, p.sha
	case TrustSchedule:
		return "cron", 0, p.ref, p.sha
	case TrustActions:
		return "manual", 0, p.ref, p.sha
	}
	return "manual", 0, "", ""
}

type provenanceJSON struct {
	Trust         TrustClass `json:"trust"`
	RepositoryID  int64      `json:"repository_id,omitempty"`
	Ref           string     `json:"ref,omitempty"`
	SHA           string     `json:"sha,omitempty"`
	Event         string     `json:"event,omitempty"`
	Pipeline      string     `json:"pipeline,omitempty"`
	DefaultBranch string     `json:"default_branch,omitempty"`
	Base          string     `json:"pull_request_base,omitempty"`
	Delivery      string     `json:"delivery,omitempty"`
	Writer        string     `json:"writer,omitempty"`
}

// MarshalJSON persists the sealed fields inside the request seal.
func (p Provenance) MarshalJSON() ([]byte, error) {
	return json.Marshal(provenanceJSON{p.trust, p.repositoryID, p.ref, p.sha, p.event, p.pipeline, p.defaultBranch, p.base, p.delivery, p.writer})
}

// UnmarshalJSON restores a stored seal and refuses any value no constructor could produce.
func (p *Provenance) UnmarshalJSON(raw []byte) error {
	var v provenanceJSON
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	decoded := Provenance{v.Trust, v.RepositoryID, v.Ref, v.SHA, v.Event, v.Pipeline, v.DefaultBranch, v.Base, v.Delivery, v.Writer}
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

// NormalizeBranch strips the refs/heads/ prefix every branch selector compares without.
func NormalizeBranch(branch string) string {
	return strings.TrimPrefix(strings.TrimSpace(branch), "refs/heads/")
}

// ParseBranchSelector normalizes patterns and refuses a malformed glob.
func ParseBranchSelector(patterns []string) (BranchSelector, error) {
	out := make(BranchSelector, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = NormalizeBranch(pattern)
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
	branch = NormalizeBranch(branch)
	for _, pattern := range s {
		if matched, err := path.Match(NormalizeBranch(pattern), branch); err == nil && matched {
			return true
		}
	}
	return false
}

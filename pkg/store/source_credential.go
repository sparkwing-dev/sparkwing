package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrSourceCredentialSpent refuses a claim a source credential when it already
// had [MaxSourceMints], or when its attempt has started running the pipeline's code.
var ErrSourceCredentialSpent = errors.New("store: this claim's source credentials were all issued or its attempt has started")

// SourceSpec is what a pipeline's plan asks of its checkout. A plan claim, and
// a plan that declares nothing, gets one commit and nothing else.
type SourceSpec struct {
	Depth      int  `json:"depth"`
	Tags       bool `json:"tags"`
	Submodules bool `json:"submodules"`
	LFS        bool `json:"lfs"`
}

type plannedSource struct {
	Depth      *int `json:"depth"`
	Tags       bool `json:"tags"`
	Submodules bool `json:"submodules"`
	LFS        bool `json:"lfs"`
}

func (p *plannedSource) spec() SourceSpec {
	s := SourceSpec{Depth: 1}
	if p == nil {
		return s
	}
	if p.Depth != nil {
		s.Depth = *p.Depth
	}
	s.Tags, s.Submodules, s.LFS = p.Tags, p.Submodules, p.LFS
	return s
}

// MaxSourceMints is how many source credentials one claim may be issued, so
// a mint that failed or whose answer was lost can be asked for again.
const MaxSourceMints = 3

// SourceSpend is what a spent source credential covers: the checkout the
// run's plan asks for.
type SourceSpend struct {
	Spec SourceSpec
}

// SpendSourceCredential records the issue of a GitHub App token to tok's
// claim as a release of rel.CredentialID on rel.Host to rel.Runner. It
// refuses a claim of another team, one that is not live or is cancelled, and,
// for a claim with a node of its own, one that spent [MaxSourceMints] or whose
// attempt has started. A work claim gets the checkout its run's plan asks for.
func (t *Tenant) SpendSourceCredential(ctx context.Context, tok ClaimToken, rel GitCredentialRelease, now time.Time) (_ SourceSpend, err error) {
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return SourceSpend{}, err
	}
	defer rollbackUnlessDone(tx, &err)
	out, err := t.spendSourceClaimTx(ctx, tx, tok, now)
	if err != nil {
		return SourceSpend{}, err
	}
	if err := t.insertSourceReleaseTx(ctx, tx, tok, rel, now); err != nil {
		return SourceSpend{}, err
	}
	return out, tx.Commit()
}

func (t *Tenant) insertSourceReleaseTx(ctx context.Context, tx *storeTx, tok ClaimToken, rel GitCredentialRelease, now time.Time) error {
	id, err := newIdentityID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO git_credential_releases
       (id, team, credential_id, host, run_id, runner, token_prefix, released_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, string(t.team), rel.CredentialID, rel.Host, tok.RunID, rel.Runner, rel.TokenPrefix, now.UTC().Unix())
	return err
}

// safety: refuses a claim of another team, one not live or cancelled, and, for a claim with a node of its
// own, one that spent [MaxSourceMints] or whose attempt started, inside the transaction that records it.
func (t *Tenant) spendSourceClaimTx(ctx context.Context, tx *storeTx, tok ClaimToken, now time.Time) (SourceSpend, error) {
	if tok.Team != t.team {
		return SourceSpend{}, ErrClaimNotLive
	}
	switch tok.Kind {
	case ClaimTokenTrigger:
		if err := fenceSensitiveTriggerClaimTx(ctx, tx, tok, now); err != nil {
			return SourceSpend{}, err
		}
	case ClaimTokenPlan, ClaimTokenWork, ClaimTokenNode:
		if err := fenceSensitiveClaimTx(ctx, tx, tok, now); err != nil {
			return SourceSpend{}, err
		}
		if err := spendNodeSourceMintTx(ctx, tx, tok); err != nil {
			return SourceSpend{}, err
		}
	default:
		return SourceSpend{}, ErrClaimNotLive
	}
	var plan []byte
	if err := tx.QueryRowContext(ctx, `SELECT plan_json FROM runs WHERE team = ? AND id = ?`,
		string(tok.Team), tok.RunID).Scan(&plan); err != nil {
		return SourceSpend{}, err
	}
	var doc struct {
		Source *plannedSource `json:"source"`
	}
	if tok.Kind == ClaimTokenWork && len(plan) > 0 {
		if err := json.Unmarshal(plan, &doc); err != nil {
			return SourceSpend{}, fmt.Errorf("read the run's plan: %w", err)
		}
	}
	return SourceSpend{Spec: doc.Source.spec()}, nil
}

// safety: a node's own process holds the same claim token as the step that
// fetches, so a claim gets a few credentials to survive a failed mint, and
// none once its attempt runs the pipeline's code.
func spendNodeSourceMintTx(ctx context.Context, tx *storeTx, tok ClaimToken) error {
	var started sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT execution_started_at FROM nodes WHERE team = ? AND run_id = ? AND node_id = ?`,
		string(tok.Team), tok.RunID, tok.NodeID).Scan(&started); err != nil {
		return err
	}
	if started.Valid {
		return ErrSourceCredentialSpent
	}
	res, err := tx.ExecContext(ctx, `UPDATE claim_tokens SET source_mints = source_mints + 1
 WHERE team = ? AND run_id = ? AND node_id = ? AND claim_generation = ? AND source_mints < ?`,
		string(tok.Team), tok.RunID, tok.NodeID, tok.Generation, MaxSourceMints)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return errors.Join(err, ErrSourceCredentialSpent)
	}
	return nil
}

// SourceClaimant names the machine credential behind a runner's node or
// trigger claim, read at the claim's own generation; a launcher's plan or
// work claim has none and gets the zero identity. ErrClaimNotLive when the
// claim moved to another generation.
func (t *Tenant) SourceClaimant(ctx context.Context, tok ClaimToken) (ClaimIdentity, error) {
	var id ClaimIdentity
	var err error
	switch {
	case tok.Team != t.team:
		return ClaimIdentity{}, ErrClaimNotLive
	case tok.Kind == ClaimTokenNode:
		err = t.s.queryRow(ctx, `SELECT COALESCE(claim_principal, ''), COALESCE(claim_token_prefix, '') FROM nodes
 WHERE team = ? AND run_id = ? AND node_id = ? AND claim_generation = ?`,
			string(t.team), tok.RunID, tok.NodeID, tok.Generation).Scan(&id.Principal, &id.TokenPrefix)
	case tok.Kind == ClaimTokenTrigger:
		err = t.s.queryRow(ctx, `SELECT COALESCE(claim_principal, ''), COALESCE(claim_token_prefix, '') FROM triggers
 WHERE team = ? AND id = ? AND claim_seq = ?`,
			string(t.team), tok.RunID, tok.Generation).Scan(&id.Principal, &id.TokenPrefix)
	default:
		return ClaimIdentity{}, nil
	}
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !id.bound()) {
		return ClaimIdentity{}, ErrClaimNotLive
	}
	return id, err
}

var sourceMintCols = map[string]string{"source_mints": "INTEGER NOT NULL DEFAULT 0"}

// safety: 0 is an extra repository listed before its ID was recorded, which
// no source credential is minted for until an owner saves the list again.
var extraRepoIDCols = map[string]string{"extra_repo_id": "INTEGER NOT NULL DEFAULT 0"}

func applySourceMintMigration(ctx context.Context, tx *storeTx, postgres bool) error {
	return addDispatchColumnsTx(ctx, tx, postgres, map[string]map[string]string{
		"claim_tokens": sourceMintCols, "github_app_extra_repos": extraRepoIDCols,
	})
}

func validPlannedSource(p *plannedSource) error {
	if p != nil && p.Depth != nil && *p.Depth < 0 {
		return errors.New("the source depth must be 0 (all history) or more")
	}
	return nil
}

// SourceCredential is the one credential a claim fetches its run's source
// with, and the commit and checkout the run asks for. Kind "github_app" is a
// GitHub App token in Token that reads exactly Repositories; "https" and
// "ssh" are the team's stored credential for Host, in Username and Secret,
// with KnownHosts pinning an ssh host key.
type SourceCredential struct {
	Kind      string `json:"kind"`
	Host      string `json:"host"`
	Token     string `json:"token,omitempty"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
	RepoURL   string `json:"repo_url,omitempty"`
	SHA       string `json:"sha,omitempty"`
	Branch    string `json:"branch,omitempty"`
	// Repositories are owner/name, the run's own first, then the ones a team
	// owner listed for it; empty for a run on a host other than github.com.
	Repositories []string   `json:"repositories"`
	Source       SourceSpec `json:"source"`
	Username     string     `json:"username,omitempty"`
	Secret       string     `json:"secret,omitempty"`
	KnownHosts   string     `json:"known_hosts,omitempty"`
}

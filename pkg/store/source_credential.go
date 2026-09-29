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
// run's plan asks for and the GitHub ID of the run's repository, 0 when the
// run's trigger recorded none.
type SourceSpend struct {
	Spec   SourceSpec
	RepoID int64
}

// SpendSourceCredential records one of the [MaxSourceMints] source credentials
// tok's claim may be issued, as a git credential release of the team's. It
// refuses a claim that is not live, is cancelled, spent them all, or whose
// attempt has started.
func (t *Tenant) SpendSourceCredential(ctx context.Context, tok ClaimToken, credentialID, host, runner string, now time.Time) (_ SourceSpend, err error) {
	if tok.Team != t.team {
		return SourceSpend{}, ErrClaimNotLive
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return SourceSpend{}, err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := fenceSensitiveClaimTx(ctx, tx, tok, now); err != nil {
		return SourceSpend{}, err
	}
	var started sql.NullInt64
	var plan []byte
	var out SourceSpend
	if err := tx.QueryRowContext(ctx, `SELECT n.execution_started_at, r.plan_json, COALESCE(tr.github_repo_id, 0) FROM nodes n
  JOIN runs r ON r.team = n.team AND r.id = n.run_id
  LEFT JOIN triggers tr ON tr.team = n.team AND tr.id = n.run_id
 WHERE n.team = ? AND n.run_id = ? AND n.node_id = ?`,
		string(tok.Team), tok.RunID, tok.NodeID).Scan(&started, &plan, &out.RepoID); err != nil {
		return SourceSpend{}, err
	}
	// safety: the user container holds the same claim token as the init
	// container that fetches, so a claim gets a few credentials to survive a
	// failed mint, and none once its attempt runs.
	if started.Valid {
		return SourceSpend{}, ErrSourceCredentialSpent
	}
	res, err := tx.ExecContext(ctx, `UPDATE claim_tokens SET source_mints = source_mints + 1
 WHERE team = ? AND run_id = ? AND node_id = ? AND claim_generation = ? AND source_mints < ?`,
		string(tok.Team), tok.RunID, tok.NodeID, tok.Generation, MaxSourceMints)
	if err != nil {
		return SourceSpend{}, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return SourceSpend{}, errors.Join(err, ErrSourceCredentialSpent)
	}
	id, err := newIdentityID()
	if err != nil {
		return SourceSpend{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO git_credential_releases
       (id, team, credential_id, host, run_id, runner, token_prefix, released_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, string(t.team), credentialID, host, tok.RunID, runner, tok.Prefix, now.UTC().Unix()); err != nil {
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
	out.Spec = doc.Source.spec()
	return out, tx.Commit()
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

// SourceCredential is what a claim's init container fetches its run's source
// with: a GitHub App token that reads exactly Repositories, as owner/name with
// the run's own first, and the commit and checkout the run asks for.
type SourceCredential struct {
	Token        string     `json:"token"`
	ExpiresAt    int64      `json:"expires_at"`
	RepoURL      string     `json:"repo_url"`
	SHA          string     `json:"sha"`
	Branch       string     `json:"branch,omitempty"`
	Repositories []string   `json:"repositories"`
	Source       SourceSpec `json:"source"`
}

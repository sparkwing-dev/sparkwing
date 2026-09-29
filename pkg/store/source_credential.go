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
// had its one, or when its attempt has started running the pipeline's code.
var ErrSourceCredentialSpent = errors.New("store: this claim's source credential was already issued or its attempt has started")

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

// SpendSourceCredential records the one source credential tok's claim may be
// issued, as a git credential release of the team's, and answers what the
// run's plan asks of its checkout. It refuses a claim that is not live, is
// cancelled, already spent its credential, or whose attempt has started.
func (t *Tenant) SpendSourceCredential(ctx context.Context, tok ClaimToken, credentialID, host, runner string, now time.Time) (_ SourceSpec, err error) {
	if tok.Team != t.team {
		return SourceSpec{}, ErrClaimNotLive
	}
	tx, err := t.s.beginTx(ctx)
	if err != nil {
		return SourceSpec{}, err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := fenceSensitiveClaimTx(ctx, tx, tok, now); err != nil {
		return SourceSpec{}, err
	}
	var created int64
	var started sql.NullInt64
	var plan []byte
	if err := tx.QueryRowContext(ctx, `SELECT c.created_at, n.execution_started_at, r.plan_json FROM claim_tokens c
  JOIN nodes n ON n.team = c.team AND n.run_id = c.run_id AND n.node_id = c.node_id
  JOIN runs r ON r.team = c.team AND r.id = c.run_id
 WHERE c.team = ? AND c.run_id = ? AND c.node_id = ? AND c.claim_generation = ?`,
		string(tok.Team), tok.RunID, tok.NodeID, tok.Generation).Scan(&created, &started, &plan); err != nil {
		return SourceSpec{}, err
	}
	// safety: the user container holds the same claim token as the init
	// container that fetches, so a claim gets one credential and none once its
	// attempt runs; the claim's own creation bounds the audit scan.
	var spent int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM git_credential_releases
 WHERE team = ? AND released_at >= ? AND run_id = ? AND token_prefix = ? LIMIT 1`,
		string(tok.Team), time.Unix(0, created).Unix(), tok.RunID, tok.Prefix).Scan(&spent)
	if err == nil || started.Valid {
		return SourceSpec{}, ErrSourceCredentialSpent
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return SourceSpec{}, err
	}
	id, err := newIdentityID()
	if err != nil {
		return SourceSpec{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO git_credential_releases
       (id, team, credential_id, host, run_id, runner, token_prefix, released_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, string(t.team), credentialID, host, tok.RunID, runner, tok.Prefix, now.UTC().Unix()); err != nil {
		return SourceSpec{}, err
	}
	var doc struct {
		Source *plannedSource `json:"source"`
	}
	if tok.Kind == ClaimTokenWork && len(plan) > 0 {
		if err := json.Unmarshal(plan, &doc); err != nil {
			return SourceSpec{}, fmt.Errorf("read the run's plan: %w", err)
		}
	}
	return doc.Source.spec(), tx.Commit()
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

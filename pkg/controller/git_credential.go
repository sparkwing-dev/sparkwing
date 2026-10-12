package controller

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: only a runner's live claim on a run of the credential's own team and
// host receives it, from a cloud runner or a machine its owner opted in. The
// claim is admitted, and the release and its audit row commit, in one
// transaction.
func (s *Server) releaseTeamGitCredential(w http.ResponseWriter, r *http.Request, src claimedRunSource, tok store.ClaimToken,
	host string, repos []string, remedy string,
) {
	ctx := r.Context()
	t := src.trigger
	stored, err := src.tenant.GitCredentialForHost(ctx, host)
	if errors.Is(err, store.ErrNotFound) {
		writeNoSourceCredential(w, remedy)
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "read git credential", err)
		return
	}
	if stored.ConfirmedAt == nil {
		writeAuthError(w, http.StatusConflict, authErrorBody{
			Code: "git_credential_unconfirmed", Principal: src.claimant.Principal,
			Message: "the team's git credential for " + host + " is unusable until a team owner confirms " +
				"its host key " + stored.Fingerprint + " (Team > Git credentials)",
		})
		return
	}
	eligible, err := s.receivesTeamGitCredentials(ctx, src.tenant, src.claimant.TokenPrefix)
	if err != nil {
		s.writeInternalError(w, r, "git credential eligibility", err)
		return
	}
	if !eligible {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code: "git_credential_not_released", Principal: src.claimant.Principal,
			Message: "the team's git credential for " + host + " is released only to cloud runners and to " +
				"machines a team owner opted in (Team > Machines)",
		})
		return
	}
	cipher, ok := s.boundCipher(w)
	if !ok {
		return
	}
	released, spend, err := src.tenant.ReleaseGitCredential(ctx, tok, s.sourceRelease(r, src, tok, "", host), time.Now())
	if errors.Is(err, store.ErrNotFound) {
		writeNoSourceCredential(w, remedy)
		return
	}
	if failure := s.sourceSpendFailure(r, err); failure != nil {
		failure.write(w)
		return
	}
	secret, err := openSecret(cipher, gitCredentialBinding(tok.Team, released.Host), released.Secret)
	if err != nil {
		s.logger.Error("git credential: open envelope", "team", string(tok.Team), "host", host, "err", err)
		writeError(w, http.StatusInternalServerError, errors.New("the stored git credential did not open"))
		return
	}
	s.logger.Info("git credential released", "team", string(tok.Team), "host", host, "kind", released.Kind,
		"run_id", t.ID, "principal", src.claimant.Principal, "token_prefix", src.claimant.TokenPrefix)
	writeJSON(w, http.StatusOK, store.SourceCredential{
		Kind: released.Kind, Host: released.Host, Username: released.Username, Secret: secret,
		KnownHosts: released.KnownHosts, SHA: t.GitSHA, Branch: t.GitBranch, Repositories: repos, Source: spend.Spec,
	})
}

// safety: Cloud runner claims are metered; machines need the owner's opt-in before receiving team git credentials.
func (s *Server) receivesTeamGitCredentials(ctx context.Context, t *store.Tenant, prefix string) (bool, error) {
	if prefix == "" {
		return false, nil
	}
	metered, err := s.store.TokenMetered(ctx, prefix)
	if err != nil || metered {
		return metered, err
	}
	return t.GitCredentialMachine(ctx, prefix)
}

func noSourceCredentialRemedy(identity, host string) string {
	msg := "no source credential covers " + identity + ": "
	if host == "github.com" {
		slug := strings.TrimPrefix(identity, "github.com/")
		return msg + "install the team's GitHub App on " + slug + " (Team > GitHub), " +
			"or store a git credential for github.com (Team > Git credentials)"
	}
	return msg + "store a git credential for " + host + " (Team > Git credentials): " +
		"an SSH deploy key or an HTTPS token"
}

// bug: No credential differs from a controller without the route, which answers its mux's plain 404.
const noSourceCredentialCode = "no_source_credential"

func writeNoSourceCredential(w http.ResponseWriter, message string) {
	writeJSON(w, http.StatusNotFound, authErrorBody{Code: noSourceCredentialCode, Message: message})
}

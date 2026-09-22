package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// GitHubWebhookBindingRequest connects one repository to one pipeline:
// the controller stores Secret as the signing secret for deliveries that
// repository sends to POST /webhooks/github/{pipeline}, and adds the
// repository to that pipeline's allow-list. Events and HookID record what
// the caller configured on GitHub so an operator can find the webhook again.
type GitHubWebhookBindingRequest struct {
	Pipeline string   `json:"pipeline"`
	Repo     string   `json:"repo"`
	Secret   string   `json:"secret"`
	Events   []string `json:"events,omitempty"`
	HookID   int64    `json:"hook_id,omitempty"`
}

// GitHubWebhookBindingResponse reports a stored binding. It never carries
// the secret: the caller supplied it, and nothing else may read it back.
// DeliveryURL is the address GitHub must post to for this pipeline, which
// is the controller's externally announced URL when it has one and the URL
// this request arrived at otherwise.
type GitHubWebhookBindingResponse struct {
	Pipeline    string   `json:"pipeline"`
	Repo        string   `json:"repo"`
	Events      []string `json:"events,omitempty"`
	HookID      int64    `json:"hook_id,omitempty"`
	DeliveryURL string   `json:"delivery_url"`
	CreatedAt   int64    `json:"created_at"`
	UpdatedAt   int64    `json:"updated_at"`
}

// GitHubWebhookDisconnectResponse reports whether a binding was there to
// remove, so a repeated disconnect is distinguishable from the first one.
// DeliveryURL and HookID name the webhook that was posting to this
// controller, so a caller deletes that webhook on GitHub rather than
// every webhook whose URL happens to name the same pipeline.
type GitHubWebhookDisconnectResponse struct {
	Pipeline    string `json:"pipeline"`
	Repo        string `json:"repo"`
	Removed     bool   `json:"removed"`
	DeliveryURL string `json:"delivery_url"`
	HookID      int64  `json:"hook_id,omitempty"`
}

// WithExternalURL declares the base URL this controller answers on from
// outside the cluster, which is what GitHub must post deliveries to. The
// bindings route announces it so `sparkwing cluster webhooks connect`
// points a repository's webhook at the right address. Empty leaves each
// binding request answering with the URL it arrived at, which is correct
// whenever the operator reaches the controller where GitHub does.
func (s *Server) WithExternalURL(rawURL string) *Server {
	s.externalURL = strings.TrimRight(strings.TrimSpace(rawURL), "/")
	return s
}

func githubWebhookDeliveryPath(pipeline string) string {
	return "/webhooks/github/" + url.PathEscape(pipeline)
}

func (s *Server) githubWebhookDeliveryURL(r *http.Request, pipeline string) string {
	base := s.externalURL
	if base == "" {
		base = requestBaseURL(r)
	}
	return base + githubWebhookDeliveryPath(pipeline)
}

// safety: a controller behind TLS termination sees a plain listener, so the
// forwarded scheme is the only thing that tells the caller's URL from the hop's.
func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := r.Header.Get("X-Forwarded-Proto"); forwarded != "" {
		if proto, _, _ := strings.Cut(forwarded, ","); proto != "" {
			scheme = strings.TrimSpace(proto)
		}
	}
	return scheme + "://" + r.Host
}

func (s *Server) handleConnectGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	var req GitHubWebhookBindingRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	pipeline, repo, err := validGitHubBinding(req.Pipeline, req.Repo)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Secret == "" {
		writeError(w, http.StatusBadRequest, errors.New("secret is required"))
		return
	}
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	stored, err := s.sealWebhookSecret(pipeline, repo, req.Secret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	binding := store.GitHubWebhookBinding{
		Pipeline: pipeline, Repo: repo, Secret: stored,
		Events: req.Events, HookID: req.HookID,
	}
	if err := tenant.PutGitHubWebhookBinding(r.Context(), binding); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	saved, err := tenant.GetGitHubWebhookBinding(r.Context(), pipeline, repo)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.logger.Info("github webhook binding stored",
		"pipeline", pipeline, "repo", repo, "hook_id", req.HookID,
		"events", strings.Join(req.Events, ","), "encrypted", s.secretsCipher != nil, "team", tenant.Team())
	writeJSON(w, http.StatusCreated, GitHubWebhookBindingResponse{
		Pipeline:    saved.Pipeline,
		Repo:        saved.Repo,
		Events:      saved.Events,
		HookID:      saved.HookID,
		DeliveryURL: s.githubWebhookDeliveryURL(r, pipeline),
		CreatedAt:   saved.CreatedAt.Unix(),
		UpdatedAt:   saved.UpdatedAt.Unix(),
	})
}

func (s *Server) handleDisconnectGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	pipeline, repo, err := validGitHubBinding(
		r.URL.Query().Get("pipeline"), r.URL.Query().Get("repo"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	var hookID int64
	if existing, err := tenant.GetGitHubWebhookBinding(r.Context(), pipeline, repo); err == nil {
		hookID = existing.HookID
	}
	removed, err := tenant.DeleteGitHubWebhookBinding(r.Context(), pipeline, repo)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.logger.Info("github webhook binding removed",
		"pipeline", pipeline, "repo", repo, "removed", removed, "hook_id", hookID)
	writeJSON(w, http.StatusOK, GitHubWebhookDisconnectResponse{
		Pipeline:    pipeline,
		Repo:        repo,
		Removed:     removed,
		DeliveryURL: s.githubWebhookDeliveryURL(r, pipeline),
		HookID:      hookID,
	})
}

func validGitHubBinding(pipeline, repo string) (string, string, error) {
	pipeline = strings.TrimSpace(pipeline)
	if pipeline == "" {
		return "", "", errors.New("pipeline is required")
	}
	// safety: the pipeline is a path segment of the delivery URL, so a name
	// that escapes the segment would bind deliveries to another route.
	if pipeline != url.PathEscape(pipeline) {
		return "", "", fmt.Errorf("pipeline %q is not a single URL path segment", pipeline)
	}
	slug, ok := normalizeGitHubRepo(strings.TrimSpace(repo))
	if !ok {
		return "", "", fmt.Errorf("repo %q is not an ascii owner/name slug", repo)
	}
	return pipeline, slug, nil
}

// safety: the stored secret verifies every future delivery, so it is sealed
// with the same cipher and binding discipline a secret row gets.
func (s *Server) sealWebhookSecret(pipeline, repo, plain string) (string, error) {
	if s.secretsCipher == nil {
		return plain, nil
	}
	sealed, err := sealSecret(s.secretsCipher, webhookSecretBinding(pipeline, repo), plain)
	if err != nil {
		return "", fmt.Errorf("seal the webhook secret: %w", err)
	}
	return sealed, nil
}

func (s *Server) openWebhookSecret(pipeline, repo, stored string) (string, error) {
	if !secrets.IsEncrypted(stored) {
		return stored, nil
	}
	if s.secretsCipher == nil {
		return "", errors.New("the binding is encrypted and no secrets key is configured")
	}
	binding := webhookSecretBinding(pipeline, repo)
	if !secrets.IsBound(stored) {
		return openLegacySecret(s.secretsCipher, binding, stored)
	}
	return openSecret(s.secretsCipher, binding, stored)
}

// safety: these routes read and write the default team's bindings, so that is
// the team the envelope is sealed to. Bindings are not resealed at startup, so
// one sealed before team binding still opens here.
func webhookSecretBinding(pipeline, repo string) secretBinding {
	return secretBinding{Team: store.DefaultTeam, Name: "github-webhook/" + pipeline, Scope: repo, Masked: true}
}

// safety: bound means a stored binding names this pipeline and repository,
// which allows the delivery whatever the document says; scoped means some
// secret narrower than the shared one exists, so an unresolved secret
// answers 401 rather than reading out the binding table.
type githubWebhookResolution struct {
	candidates []githubWebhookCandidate
	bound      bool
	scoped     bool
}

// githubWebhookCandidate is a secret that may have signed a delivery and the
// team a delivery it verifies runs in.
type githubWebhookCandidate struct {
	secret string
	team   store.Team
}

// safety: the stored bindings only add. A pipeline the document leaves
// unchecked stays unchecked, so connecting one repository cannot start
// refusing deliveries an existing install already accepts. A stored binding
// that names the repository shuts out the document's secrets for it, as it did
// before bindings carried a team, and the document and the shared secret
// belong to the team every pre-tenant row was migrated into.
func (s *Server) resolveGitHubWebhook(ctx context.Context, pipeline, repo string) githubWebhookResolution {
	res := githubWebhookResolution{scoped: s.githubWebhookHasScopedSecret()}
	for _, b := range s.storedGitHubBindings(ctx, pipeline) {
		res.scoped = true
		if repo == "" || b.Repo != repo {
			continue
		}
		plain, err := s.openWebhookSecret(pipeline, b.Repo, b.Secret)
		if err != nil {
			s.logger.Error("github webhook binding unreadable",
				"pipeline", pipeline, "repo", b.Repo, "team", b.Team, "err", err)
			continue
		}
		res.candidates = append(res.candidates, githubWebhookCandidate{secret: plain, team: b.Team})
		res.bound = true
	}
	if len(res.candidates) == 0 {
		if secret := s.githubWebhookSecretFor(pipeline, repo); secret != "" {
			res.candidates = append(res.candidates, githubWebhookCandidate{secret: secret, team: store.DefaultTeam})
		}
	}
	return res
}

// verifiedGitHubTeam reports the team whose secret signed the delivery. The
// signature is the only credential a delivery carries, so it is what picks the
// team; a signature two teams' secrets both verify names no one team and is
// refused rather than handed to whichever was read first.
func (s *Server) verifiedGitHubTeam(res githubWebhookResolution, signature string, body []byte, pipeline string) (store.Team, bool) {
	var team store.Team
	verified := false
	for _, c := range res.candidates {
		if !verifyGitHubSignature(signature, body, c.secret) {
			continue
		}
		if verified && c.team != team {
			s.logger.Error("github webhook refused",
				"pipeline", pipeline, "reason", "the signature verifies against more than one team's binding")
			return "", false
		}
		team, verified = c.team, true
	}
	return team, verified
}

func (s *Server) storedGitHubBindings(ctx context.Context, pipeline string) []store.GitHubWebhookBinding {
	if s.store == nil {
		return nil
	}
	bindings, err := s.store.AsOperator().ListGitHubWebhookBindingsAcrossTeams(ctx, pipeline)
	if err != nil {
		// safety: a store that cannot answer leaves the document in charge
		// rather than refusing deliveries it has always accepted.
		s.logger.Error("read github webhook bindings", "pipeline", pipeline, "err", err)
		return nil
	}
	return bindings
}

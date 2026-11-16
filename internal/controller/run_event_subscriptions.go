package controller

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runevents"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

// RunEventSubscriptionStore is the tenant port the subscription routes use.
type RunEventSubscriptionStore interface {
	Team() store.Team
	RunEventSubscriptions(context.Context) ([]store.RunEventSubscription, error)
	RunEventSubscription(context.Context, string) (store.RunEventSubscription, error)
	CreateRunEventSubscription(context.Context, capability.Authorized, store.SealedRunEventSubscription) (store.RunEventSubscription, bool, error)
	DeleteRunEventSubscription(context.Context, capability.Authorized, string) (bool, error)
	RotateRunEventSubscriptionSecret(context.Context, capability.Authorized, store.SealedRunEventSubscription) (store.RunEventSubscription, error)
}

var _ RunEventSubscriptionStore = (*store.Tenant)(nil)

const (
	maxSubscriptionURL         = 2048
	maxSubscriptionDescription = 200
)

func (s *Server) subscriptionEndpoints() []controllerwire.Endpoint {
	return []controllerwire.Endpoint{
		{Method: "GET", Pattern: "/api/v1/subscriptions", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleListSubscriptions},
		{Method: "POST", Pattern: "/api/v1/subscriptions", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleCreateSubscription},
		{Method: "GET", Pattern: "/api/v1/subscriptions/{subscription}", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleGetSubscription},
		{Method: "DELETE", Pattern: "/api/v1/subscriptions/{subscription}", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleDeleteSubscription},
		{Method: "POST", Pattern: "/api/v1/subscriptions/{subscription}/rotate-secret", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleRotateSubscriptionSecret},
	}
}

// OpenRunEventSecret returns the plaintext "whsec_" signing secret of one subscription.
func OpenRunEventSecret(c Cipher, team store.Team, sub store.RunEventSubscription) (string, error) {
	if sub.Team != team {
		return "", errors.New("subscription belongs to another team")
	}
	return openSecret(c, runEventSecretBinding(team, sub.ID), sub.Secret)
}

// safety: the envelope is bound to its team and subscription ID, so a row copied to another team or ID does not open.
func runEventSecretBinding(team store.Team, id string) secretBinding {
	return secretBinding{Purpose: secrets.PurposeEventSigning, Team: team, Name: store.RunEventSecretName(id), Masked: true}
}

func (s *Server) subscriptionStore(ctx context.Context, team string) (RunEventSubscriptionStore, error) {
	return tenantPort[RunEventSubscriptionStore](ctx, s, store.Team(team))
}

func subscriptionView(row store.RunEventSubscription) controllerwire.Subscription {
	return controllerwire.Subscription{
		ID: row.ID, URL: row.URL, EventTypes: row.Filter.EventTypes, Statuses: row.Filter.Statuses,
		Pipelines: row.Filter.Pipelines, Repositories: row.Filter.Repositories, Description: row.Description,
		CreatedBy: row.CreatedPrincipal, CreatedAt: row.CreatedAt.Unix(), UpdatedAt: row.UpdatedAt.Unix(),
	}
}

func (s *Server) handleListSubscriptions(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	t, err := s.subscriptionStore(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	rows, err := t.RunEventSubscriptions(r.Context())
	if err != nil {
		return engineLookupError(err)
	}
	out := make([]controllerwire.Subscription, 0, len(rows))
	for _, row := range rows {
		out = append(out, subscriptionView(row))
	}
	return writeListPage(w, r, a, "subscriptions", out, func(row controllerwire.Subscription) string { return row.ID }, nil)
}

func (s *Server) handleGetSubscription(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	t, err := s.subscriptionStore(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	row, err := t.RunEventSubscription(r.Context(), r.PathValue("subscription"))
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, subscriptionView(row))
	return nil
}

func (s *Server) handleCreateSubscription(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	req, err := decodeCredentialRequest[controllerwire.SubscriptionWrite](r)
	if err != nil {
		return err
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 200 {
		return invalidEngineBody("creating a subscription requires an Idempotency-Key header of at most 200 characters")
	}
	target, err := validSubscriptionURL(req.URL, s.dashboard != nil && s.dashboard.Local)
	if err != nil {
		return invalidEngineBody(err.Error())
	}
	filter := runevents.Filter{EventTypes: req.EventTypes, Statuses: req.Statuses, Pipelines: req.Pipelines, Repositories: req.Repositories}
	switch {
	case len(filter.EventTypes) > 0:
	case len(filter.Statuses) > 0:
		filter.EventTypes = []string{runevents.EventFinished}
	default:
		filter.EventTypes = []string{runevents.EventStarted, runevents.EventFinished}
	}
	if err := filter.Validate(); err != nil {
		return invalidEngineBody(err.Error())
	}
	if utf8.RuneCountInString(req.Description) > maxSubscriptionDescription || strings.IndexFunc(req.Description, isControlRune) >= 0 {
		return invalidEngineBody(fmt.Sprintf("description is at most %d characters with no control characters", maxSubscriptionDescription))
	}
	cipher, ok := s.secretsCipher.(BoundCipher)
	if !ok {
		return &apierr.Error{Code: hosterr.Unavailable, Message: "bound secret encryption is unavailable", Details: map[string]any{"reason": hosterr.ReasonSecretsKeyRequired}}
	}
	t, err := s.subscriptionStore(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	secret, err := newSigningSecret()
	if err != nil {
		return err
	}
	row := store.RunEventSubscription{ID: store.RunEventSubscriptionID(t.Team(), key), URL: target, Filter: filter, Description: req.Description, Secret: secret}
	sealed, err := store.SealRunEventSubscription(cipher, t.Team(), row)
	if err != nil {
		return credentialWriteError(err)
	}
	stored, replayed, err := t.CreateRunEventSubscription(r.Context(), a, sealed)
	if err != nil {
		return subscriptionWriteError(err)
	}
	if replayed {
		if secret, err = OpenRunEventSecret(cipher, t.Team(), stored); err != nil {
			return credentialWriteError(err)
		}
	}
	writeJSON(w, http.StatusOK, controllerwire.SubscriptionSecret{Subscription: subscriptionView(stored), Secret: secret, Replayed: replayed})
	return nil
}

func (s *Server) handleDeleteSubscription(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	t, err := s.subscriptionStore(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	id := r.PathValue("subscription")
	deleted, err := t.DeleteRunEventSubscription(r.Context(), a, id)
	if err != nil {
		return subscriptionWriteError(err)
	}
	writeJSON(w, http.StatusOK, controllerwire.SubscriptionDeletion{ID: id, Deleted: deleted})
	return nil
}

func (s *Server) handleRotateSubscriptionSecret(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	cipher, ok := s.secretsCipher.(BoundCipher)
	if !ok {
		return &apierr.Error{Code: hosterr.Unavailable, Message: "bound secret encryption is unavailable", Details: map[string]any{"reason": hosterr.ReasonSecretsKeyRequired}}
	}
	t, err := s.subscriptionStore(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	secret, err := newSigningSecret()
	if err != nil {
		return err
	}
	sealed, err := store.SealRunEventSubscription(cipher, t.Team(), store.RunEventSubscription{ID: r.PathValue("subscription"), Secret: secret})
	if err != nil {
		return credentialWriteError(err)
	}
	stored, err := t.RotateRunEventSubscriptionSecret(r.Context(), a, sealed)
	if err != nil {
		return subscriptionWriteError(err)
	}
	writeJSON(w, http.StatusOK, controllerwire.SubscriptionSecret{Subscription: subscriptionView(stored), Secret: secret})
	return nil
}

func subscriptionWriteError(err error) error {
	switch {
	case errors.Is(err, store.ErrRunEventSubscriptionLimit):
		return &apierr.Error{
			Code: apierr.Conflict, Message: strings.TrimPrefix(err.Error(), "store: "),
			Hint: "list them with `sparkwing subscriptions list` and remove one with `sparkwing subscriptions delete --subscription <id>`",
		}
	case errors.Is(err, store.ErrRunEventSubscriptionConflict):
		return &apierr.Error{Code: apierr.Conflict, Message: strings.TrimPrefix(err.Error(), "store: ")}
	default:
		return credentialWriteError(err)
	}
}

// safety: Standard Webhooks libraries expect "whsec_" and base64 key bytes;
// 32 bytes matches their HMAC-SHA256 key size.
func newSigningSecret() (string, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return "whsec_" + base64.StdEncoding.EncodeToString(key), nil
}

// safety: only local mode admits http and loopback, the developer's own receiver;
// on a controller they would reach its private network.
func validSubscriptionURL(raw string, local bool) (string, error) {
	if raw == "" || len(raw) > maxSubscriptionURL || strings.IndexFunc(raw, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0 {
		return "", fmt.Errorf("url is required, at most %d characters, with no spaces or control characters", maxSubscriptionURL)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Host == "" || u.Hostname() == "" {
		return "", errors.New("url must be an absolute http or https URL with a host")
	}
	if u.User != nil {
		return "", errors.New("url must not carry a username or password")
	}
	if strings.Contains(raw, "#") {
		return "", errors.New("url must not carry a fragment")
	}
	if port := u.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return "", errors.New("url port must be between 1 and 65535")
		}
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && local:
	case local:
		return "", errors.New("url scheme must be http or https")
	default:
		return "", errors.New("url scheme must be https")
	}
	if !local {
		if err := sourceurl.CheckPublicHost(u.Hostname()); err != nil {
			return "", fmt.Errorf("url host must be a public name or address: %w", err)
		}
	}
	return raw, nil
}

func isControlRune(r rune) bool { return r < ' ' || r == 0x7f }

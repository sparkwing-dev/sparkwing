package controller

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: a link flow is a few redirects long; a state that outlives that is
// one someone kept, not one a browser is using.
const identityLinkStateTTL = 10 * time.Minute

// safety: each attempt costs the provider a code exchange and a user read, so
// one account cannot drive a stream of them.
const identityLinkAttemptsPerMinute = 10

// safety: a session left open on a shared machine must not be able to add a
// way into the account or remove its owner's, so both ask for a fresh sign-in.
const identityChangeSignInWindow = 10 * time.Minute

var providerLabels = map[string]string{store.ProviderGoogle: "Google", store.ProviderGitHub: "GitHub"}

type identityJSON struct {
	Provider  string `json:"provider"`
	Email     string `json:"email"`
	CreatedAt int64  `json:"created_at"`
}

type identitiesResp struct {
	Identities []identityJSON `json:"identities"`
	Providers  []string       `json:"providers"`
}

func identityOut(id store.Identity) identityJSON {
	return identityJSON{Provider: id.Provider, Email: id.Email, CreatedAt: id.CreatedAt.Unix()}
}

func (s *Server) handleIdentities(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	reader, err := s.accountReader()
	if err != nil {
		return err
	}
	ids, err := reader.AccountIdentities(r.Context(), a.Principal.Capability.Subject.ID)
	if err != nil {
		return engineLookupError(err)
	}
	out := identitiesResp{Identities: []identityJSON{}, Providers: s.signInProviders()}
	for _, id := range ids {
		out.Identities = append(out.Identities, identityOut(id))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type identityLinkState struct {
	Account  string `json:"a"`
	Session  string `json:"s"`
	Provider string `json:"p"`
	Expires  int64  `json:"e"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
}

// safety: the state travels through the provider's URLs, so it names the
// session by a MAC only this controller can compute, never by its id.
func sessionBinding(key []byte, sessionID string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("session\x00" + sessionID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) handleIdentityUnlink(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	name := r.PathValue("provider")
	if _, known := providerLabels[name]; !known {
		return &apierr.Error{Code: apierr.NotFound, Message: "sign-in provider not found"}
	}
	writer, err := s.accountWriter()
	if err != nil {
		return err
	}
	ended, err := writer.UnlinkAccountIdentity(r.Context(), a, name)
	if err != nil {
		return accountMutationError(err)
	}
	s.logger.Info("identity.unlinked", "account", a.Principal.Capability.Subject.ID, "provider", name, "sessions_ended", ended)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

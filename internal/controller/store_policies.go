package controller

import (
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/storage"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// StorePolicies snapshots the configured dependencies for store construction.
func (s *Server) StorePolicies() store.Policies {
	policies := store.Policies{Intake: s, Console: s.consoleAuthority, AuditDigests: unboundRunCipher{}}
	if s.secretsCipher != nil {
		policies.PlanSecrets = planSecretCipher{cipher: s.secretsCipher}
		if digests, ok := s.secretsCipher.(store.OIDCAuditDigester); ok {
			policies.AuditDigests = digests
		}
		if bound, ok := s.secretsCipher.(store.SessionKeyCipher); ok {
			policies.SessionKeys, policies.RunEnvironment = bound, bound
		} else {
			// safety: a configured key that cannot bind rows refuses run arguments rather than storing them in plaintext.
			policies.RunEnvironment = unboundRunCipher{}
		}
	}
	if s.dataController != nil && s.dataController.Objects != nil {
		policies.ResultBlobs = storage.BlobManifests{Objects: s.dataController.Objects}
	}
	return policies
}

var errUnboundCipher = errors.New("the configured secrets cipher cannot bind stored rows")

type unboundRunCipher struct{}

func (unboundRunCipher) SealBound(secrets.Purpose, string, string, string, bool, bool, string) (string, error) {
	return "", errUnboundCipher
}

func (unboundRunCipher) OpenBound(secrets.Purpose, string, string, string, bool, bool, string) (string, error) {
	return "", errUnboundCipher
}

func (unboundRunCipher) OIDCAuditDigest(string, string, string) (string, string, error) {
	return "", "", errors.New("OIDC audit privacy migration requires the external secrets keyring")
}

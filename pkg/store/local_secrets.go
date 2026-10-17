package store

import (
	"maps"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/secretname"
)

type localSecretKey struct {
	team Team
	run  string
}
type localRunSecrets struct{ values map[string]string }

func validateLocalSecrets(names []string, values map[string]string) error {
	if len(names) != len(values) {
		return authorityError(apierr.BadRequest, "local secret names and resolved values must match")
	}
	seen := map[string]bool{}
	for _, name := range names {
		if secretname.Validate(name) != nil || seen[name] {
			return authorityError(apierr.BadRequest, "invalid or repeated local secret name")
		}
		if _, ok := values[name]; !ok {
			return authorityError(apierr.BadRequest, "local secret names and resolved values must match")
		}
		seen[name] = true
	}
	return nil
}

func (s *Store) installLocalSecrets(team Team, admitted AdmissionResult, values map[string]string) (*localRunSecrets, error) {
	names := admitted.Request.Request.LocalSecretNames
	if len(names) == 0 {
		return nil, nil
	}
	if admitted.Request.RunID != admitted.RunID || len(values) == 0 {
		return nil, authorityError(apierr.Unavailable, "requester local secrets must be resolved again; submit a new run")
	}
	if err := validateLocalSecrets(names, values); err != nil {
		return nil, err
	}
	key := localSecretKey{team, admitted.RunID}
	if admitted.Replayed {
		if _, present := s.localSecrets.Load(key); !present {
			return nil, authorityError(apierr.Unavailable, "requester local secrets are unavailable; submit a new run")
		}
	}
	entry := &localRunSecrets{values: maps.Clone(values)}
	previous, loaded := s.localSecrets.LoadOrStore(key, entry)
	if !loaded {
		return entry, nil
	}
	if !maps.Equal(previous.(*localRunSecrets).values, values) {
		return nil, authorityError(apierr.SpecDrift, "local secret values differ from the original intake")
	}
	return nil, nil
}

func (s *Store) localSecret(team Team, run, name string) (string, error) {
	entry, ok := s.localSecrets.Load(localSecretKey{team, run})
	if ok {
		if value, exists := entry.(*localRunSecrets).values[name]; exists {
			return value, nil
		}
	}
	return "", authorityError(apierr.Unavailable, "requester local secrets are unavailable; submit a new run")
}

func localSecretDeclared(specNames, localNames []string) bool {
	for _, name := range localNames {
		if !slices.Contains(specNames, name) {
			return false
		}
	}
	return true
}

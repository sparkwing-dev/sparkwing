package store

import (
	"encoding/json"
	"errors"
	"maps"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

// safety: the envelope binds team, run and document kind, so a document copied to another row does not open.
const sealedArgumentsKey = "sealed_arguments"

const (
	requestArgumentsScope = "request"
	specArgumentsScope    = "spec"
)

func expansionArgumentsScope(node string) string { return "expansion:" + node }

// KeylessCipher marks the cipher of a deployment that holds no secrets key.
// A store whose run cipher is keyless, or absent, keeps run argument values in
// plaintext; every other cipher seals them and fails closed when it cannot.
type KeylessCipher interface {
	SessionKeyCipher
	Keyless()
}

func storesPlaintextArguments(cipher SessionKeyCipher) bool {
	if cipher == nil {
		return true
	}
	_, keyless := cipher.(KeylessCipher)
	return keyless
}

// safety: every value a stored document carries needs a slot, or it is written in plaintext.
type argumentSlots struct {
	maps    []map[string]string
	strings []*string
}

type argumentValues struct {
	Maps    []map[string]string `json:"maps"`
	Strings []string            `json:"strings"`
}

func requestArgumentSlots(r *runspec.SealedRequest) argumentSlots {
	return argumentSlots{maps: []map[string]string{r.Request.RequestedArgs}}
}

func specArgumentSlots(s *runspec.Spec) argumentSlots {
	slots := requestArgumentSlots(&s.Request)
	slots.maps = append(slots.maps, s.Resolved.Args)
	for i := range s.Resolved.ArgumentDeclarations {
		if value := s.Resolved.ArgumentDeclarations[i].Default; value != nil {
			slots.strings = append(slots.strings, value)
		}
	}
	slots.addRegistration(&s.Resolved.SecretRegistration)
	return slots
}

func registrationArgumentSlots(r *runspec.SecretRegistration) argumentSlots {
	var slots argumentSlots
	slots.addRegistration(r)
	return slots
}

func (a *argumentSlots) addRegistration(r *runspec.SecretRegistration) {
	for i := range r.Defaults {
		a.strings = append(a.strings, &r.Defaults[i].Raw)
	}
	for i := range r.Values {
		a.strings = append(a.strings, &r.Values[i])
	}
}

func (a argumentSlots) take() (argumentValues, bool) {
	var values argumentValues
	present := false
	for _, m := range a.maps {
		values.Maps = append(values.Maps, maps.Clone(m))
		for name, value := range m {
			present = present || value != ""
			m[name] = ""
		}
	}
	for _, value := range a.strings {
		present = present || *value != ""
		values.Strings = append(values.Strings, *value)
		*value = ""
	}
	return values, present
}

// safety: values whose shape differs from the document's empty slots are refused, never merged.
func (a argumentSlots) restore(values argumentValues) bool {
	if len(values.Maps) != len(a.maps) || len(values.Strings) != len(a.strings) {
		return false
	}
	for i, m := range a.maps {
		if len(m) != len(values.Maps[i]) {
			return false
		}
		for name := range values.Maps[i] {
			if stored, ok := m[name]; !ok || stored != "" {
				return false
			}
		}
		maps.Copy(m, values.Maps[i])
	}
	for i, value := range a.strings {
		if *value != "" {
			return false
		}
		*value = values.Strings[i]
	}
	return true
}

// safety: a document without argument values stays plain JSON and reports sealed false.
func encodeRunDocument[T any](cipher SessionKeyCipher, team Team, run, scope string, doc T, slots func(*T) argumentSlots) (stored []byte, sealed bool, err error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, false, err
	}
	var blank T
	if err := json.Unmarshal(raw, &blank); err != nil {
		return nil, false, err
	}
	values, present := slots(&blank).take()
	if !present || storesPlaintextArguments(cipher) {
		return raw, false, nil
	}
	plain, err := json.Marshal(values)
	if err != nil {
		return nil, false, err
	}
	envelope, err := cipher.SealBound(secrets.PurposeRunArguments, string(team), run, scope, false, true, string(plain))
	if err != nil || !secrets.IsBound(envelope) {
		return nil, false, authorityError(hosterr.Unavailable, "run argument encryption failed")
	}
	stored, err = withSealedArguments(blank, envelope)
	return stored, err == nil, err
}

func withSealedArguments(doc any, envelope string) ([]byte, error) {
	blank, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return replaceSealedArguments(blank, envelope)
}

func replaceSealedArguments(raw []byte, envelope string) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	var err error
	if fields[sealedArgumentsKey], err = json.Marshal(envelope); err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}

// hack: documents stored before argument sealing carry no envelope and decode as written.
func decodeRunDocument[T any](s *Store, team Team, run, scope string, raw []byte, dst *T, slots func(*T) argumentSlots) error {
	if err := json.Unmarshal(raw, dst); err != nil {
		return err
	}
	envelope, err := sealedArgumentsEnvelope(raw)
	if err != nil || envelope == "" {
		return err
	}
	values, err := openRunArguments(s.runEnvCipher, team, run, scope, envelope)
	if err == nil && !slots(dst).restore(values) {
		err = corruptRunArguments()
	}
	return err
}

func sealedArgumentsEnvelope(raw []byte) (string, error) {
	var sealed struct {
		Envelope string `json:"sealed_arguments"`
	}
	if err := json.Unmarshal(raw, &sealed); err != nil {
		return "", err
	}
	return sealed.Envelope, nil
}

func openRunArguments(cipher SessionKeyCipher, team Team, run, scope, envelope string) (argumentValues, error) {
	var values argumentValues
	if cipher == nil {
		return values, authorityError(hosterr.Unavailable, "run argument encryption is unavailable")
	}
	plain, err := cipher.OpenBound(secrets.PurposeRunArguments, string(team), run, scope, false, true, envelope)
	if errors.Is(err, secrets.ErrKeyRefused) {
		return values, authorityError(hosterr.Unavailable, err.Error())
	}
	if err != nil || json.Unmarshal([]byte(plain), &values) != nil {
		return values, corruptRunArguments()
	}
	return values, nil
}

func corruptRunArguments() error {
	return authorityError(hosterr.IntegrityFailure, "sealed run arguments are unavailable or corrupt")
}

func encodeRunRequest(s *Store, team Team, run string, request runspec.SealedRequest) ([]byte, error) {
	stored, _, err := encodeRunDocument(s.runEnvCipher, team, run, requestArgumentsScope, request, requestArgumentSlots)
	return stored, err
}

func decodeRunRequest(s *Store, team Team, run string, raw []byte, dst *runspec.SealedRequest) error {
	return decodeRunDocument(s, team, run, requestArgumentsScope, raw, dst, requestArgumentSlots)
}

func encodeRunSpec(s *Store, team Team, run string, spec runspec.Spec) ([]byte, error) {
	stored, _, err := encodeRunDocument(s.runEnvCipher, team, run, specArgumentsScope, spec, specArgumentSlots)
	return stored, err
}

func encodeExpansionRegistration(s *Store, team Team, run, node string, registration runspec.SecretRegistration) ([]byte, error) {
	stored, _, err := encodeRunDocument(s.runEnvCipher, team, run, expansionArgumentsScope(node), registration, registrationArgumentSlots)
	return stored, err
}

func decodeRunSpec(s *Store, team Team, run string, raw []byte, dst *runspec.Spec) error {
	return decodeRunDocument(s, team, run, specArgumentsScope, raw, dst, specArgumentSlots)
}

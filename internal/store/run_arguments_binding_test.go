package store

import (
	"errors"
	"strings"
	"testing"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

const bindingCanary = "binding-canary-91f2"

func bindingStore(t *testing.T) *Store {
	t.Helper()
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	return &Store{runEnvCipher: cipher}
}

func TestRunArgumentEnvelopeOpensOnlyUnderItsTeamRunKindAndNode(t *testing.T) {
	s := bindingStore(t)
	reg := runspec.SecretRegistration{Version: 1, Defaults: []runspec.SecretValue{{Raw: bindingCanary}}}
	stored, err := encodeExpansionRegistration(s, "team-a", "run-1", "node-a", reg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), bindingCanary) {
		t.Fatalf("plaintext stored: %s", stored)
	}
	open := func(team Team, run, scope string) error {
		var out runspec.SecretRegistration
		err := decodeRunDocument(s, team, run, scope, stored, &out, registrationArgumentSlots)
		if err == nil && (len(out.Defaults) != 1 || out.Defaults[0].Raw != bindingCanary) {
			t.Fatalf("round trip lost value: %+v", out)
		}
		if err != nil && (strings.Contains(err.Error(), bindingCanary) || len(out.Defaults) == 1 && out.Defaults[0].Raw != "") {
			t.Fatalf("refusal leaked a value: %v %+v", err, out)
		}
		return err
	}
	if err := open("team-a", "run-1", expansionArgumentsScope("node-a")); err != nil {
		t.Fatalf("same binding refused: %v", err)
	}
	for name, c := range map[string]struct {
		team       Team
		run, scope string
	}{
		"other team":           {"team-b", "run-1", expansionArgumentsScope("node-a")},
		"other run":            {"team-a", "run-2", expansionArgumentsScope("node-a")},
		"other expansion node": {"team-a", "run-1", expansionArgumentsScope("node-b")},
		"request kind":         {"team-a", "run-1", requestArgumentsScope},
		"spec kind":            {"team-a", "run-1", specArgumentsScope},
	} {
		err := open(c.team, c.run, c.scope)
		var typed *apierr.Error
		if !errors.As(err, &typed) || typed.Code != hosterr.IntegrityFailure {
			t.Errorf("%s: want integrity failure, got %v", name, err)
		}
	}
}

func TestRunArgumentRequestRefusesASpecEnvelope(t *testing.T) {
	s := bindingStore(t)
	var req runspec.SealedRequest
	req.Request.RequestedArgs = map[string]string{"k": bindingCanary}
	stored, err := encodeRunRequest(s, "team-a", "run-1", req)
	if err != nil {
		t.Fatal(err)
	}
	var back runspec.SealedRequest
	if err := decodeRunRequest(s, "team-a", "run-1", stored, &back); err != nil || back.Request.RequestedArgs["k"] != bindingCanary {
		t.Fatalf("round trip: %v %v", back.Request.RequestedArgs, err)
	}
	var spec runspec.Spec
	spec.Request = req
	specStored, err := encodeRunSpec(s, "team-a", "run-1", spec)
	if err != nil {
		t.Fatal(err)
	}
	var wrong runspec.SealedRequest
	err = decodeRunRequest(s, "team-a", "run-1", specStored, &wrong)
	if err == nil || wrong.Request.RequestedArgs["k"] == bindingCanary {
		t.Fatalf("spec document opened as request: %v %v", wrong.Request.RequestedArgs, err)
	}
}

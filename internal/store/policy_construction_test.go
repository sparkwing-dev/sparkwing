package store

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestStorePoliciesAreConstructorOnly(t *testing.T) {
	for _, method := range []string{
		"SetIntakePolicy", "SetProvisionPolicy", "SetConsolePolicy", "SetClaimProfileResolver",
		"SetPlanSecretOpener", "SetSessionKeyCipher", "SetRunEnvironmentCipher", "SetResultBlobRecords",
	} {
		if _, ok := reflect.TypeFor[*Store]().MethodByName(method); ok {
			t.Fatalf("serving store exposes policy mutation: %s", method)
		}
	}
	calls := 0
	st, err := Open(filepath.Join(t.TempDir(), "state.db"), WithPolicies(func(st *Store) (Policies, error) {
		calls++
		_, err := st.ForTeam(t.Context(), DefaultTeam)
		return Policies{}, err
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if calls != 1 {
		t.Fatalf("policy factory called %d times", calls)
	}
}

func TestFailedPolicyConstructionClosesStore(t *testing.T) {
	want := errors.New("assembly failed")
	var constructing *Store
	st, err := Open(filepath.Join(t.TempDir(), "state.db"), WithPolicies(func(st *Store) (Policies, error) {
		constructing = st
		return Policies{}, want
	}))
	if st != nil || !errors.Is(err, want) {
		t.Fatalf("open = %v, %v", st, err)
	}
	if constructing == nil || constructing.AsOperator().Ping(t.Context()) == nil {
		t.Fatal("failed constructor left its database open")
	}
}

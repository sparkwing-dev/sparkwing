package teststore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// OperatorAuthorized returns deployment authority over limits and object
// storage, targeting team, for the store's authorized operator writes.
func OperatorAuthorized(t T, st *store.Store, team store.Team) capability.Authorized {
	t.Helper()
	_, root, err := ProvisionCredential(t.Context(), st, capability.ProvisionSpec{Kind: capability.ProvisionBootstrap})
	if err != nil {
		t.Fatal(err)
	}
	return capability.Authorized{
		Principal: capability.Principal{Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: root.Prefix}, Capability: *root},
		Target:    capability.Target{Team: string(team)},
	}
}

// Console returns a console bound to one operator account and that account's
// direct session in team, for the store's console writes.
func Console(t T, st *store.Store, team store.Team, account string) (*store.Console, capability.Authorized) {
	t.Helper()
	if err := seedCredentialMember(t.Context(), st, string(team), account, Now(st)); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("fixture-session:" + string(team) + ":" + account))
	a := capability.Authorized{
		Principal: capability.Principal{Class: capability.CredentialConsole, Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}, Capability: capability.Capability{Subject: capability.Subject{Kind: capability.SubjectAccount, ID: account}}},
		Target:    capability.Target{Team: string(team)},
	}
	return st.AsConsole(func(id string) bool { return id == account }), a
}

// safety: a store whose console policy names operator accounts must name this one.
const fixtureOperator = "fixture-operator"

// SetCreditSettings commits up through a fixture operator's console session.
func SetCreditSettings(t T, st *store.Store, up store.CreditSettingsUpdate) store.CreditSettings {
	t.Helper()
	SeedTeam(t, st, fixtureOperatorTeam)
	console, a := Console(t, st, fixtureOperatorTeam, fixtureOperator)
	out, err := console.ConsoleSetCreditSettings(t.Context(), a, up)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

const fixtureOperatorTeam store.Team = "fixture-operator"

// SeedCreditRates installs table as the deployment's rate table, for stores
// whose console policy no fixture session satisfies.
func SeedCreditRates(t T, st *store.Store, table store.CreditRateTable) {
	t.Helper()
	if err := table.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(table)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Exec(t.Context(), st, `INSERT INTO settings(key,value) VALUES ('credit_rate_table',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, raw); err != nil {
		t.Fatal(err)
	}
}

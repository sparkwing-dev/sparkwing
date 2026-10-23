package store

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

func TestCapabilitySessionRefusesDataScopeProperty(t *testing.T) {
	s, account, _ := sessionRuntimeStore(t)
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	s.csrfCipher, s.csrfKey = cipher, nil
	if _, err := s.AsOperator().csrfSigningKey(t.Context()); err != nil {
		t.Fatal(err)
	}
	random := rand.New(rand.NewPCG(73, 902))
	for i := range 32 {
		root := authorityRoot(fmt.Sprintf("scoped-%d", i), "a")
		root.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: account.ID}
		root.Grants = []capability.Grant{capability.RunsRead, capability.DataRead}
		root.Data = &capability.DataScope{Use: capability.DataRemoteCache, RepositoryKey: fmt.Sprintf("gh:%d", random.Uint64()), ReadRefs: []string{fmt.Sprintf("ref-%x", random.Uint64())}, VerifiedDefaultOnly: true}
		authorityPersist(t, s, root)
		raw, session, err := s.AsOperator().CreateCapabilitySession(t.Context(), root.Prefix, time.Hour)
		var failure *apierr.Error
		if !errors.As(err, &failure) || failure.Code != apierr.Forbidden || raw != "" || session != nil {
			t.Fatalf("data-scoped credential signed in: session=%v err=%v", session != nil, err)
		}
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("refused sign-in persisted sessions: count=%d err=%v", count, err)
	}
}

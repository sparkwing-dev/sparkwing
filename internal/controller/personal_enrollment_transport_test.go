package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestPersonalEnrollmentTransportNeverPromotesCapabilityAncestryProperty(t *testing.T) {
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	e, _ := engineServerFixture(t, deploymenttest.Team(t), func(s *Server) { s.WithSecretsCipher(cipher) })
	engineSQL(t, e.Store, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('account','a@example.test',1,1)`)
	engineSQL(t, e.Store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','account','owner',1)`)
	server := e.Server
	raw, _, _, err := e.Store.AsOperator().CreateAccountSession(t.Context(), store.Account{ID: "account"}, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(raw))
	authority := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
	options := client.EngineOptions{URL: "https://controller.test", HTTP: &http.Client{Transport: localHostRoundTripper{handler: server.Handler()}}}
	property := func(input [12]byte, administrative, foreign bool) bool {
		grants := []capability.Grant{capability.RunsRead, capability.RunsWrite}
		if administrative {
			grants = append(grants, capability.TeamAdmin)
		}
		member, err := e.Store.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMember, Authority: &authority, Team: "a", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "account"}, Grants: grants, Binding: capability.Binding{Kind: capability.BindingNone}})
		if err != nil {
			t.Fatal(err)
		}
		presentation, _, err := e.Store.AsOperator().CreateCapabilitySession(t.Context(), member.Capability.Prefix, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if foreign {
			presentation = "foreign-session-" + hex.EncodeToString(input[:])
		}
		manager, err := client.NewSessionRunnerTokenManager(t.Context(), options, presentation)
		if err == nil {
			_, err = manager.EnrollRunner(t.Context(), controllerwire.EnrollRunnerRequest{Name: "runner-" + hex.EncodeToString(input[:]), Repos: []string{"github.com/acme/*"}, Personal: true})
		}
		var refused *apierr.Error
		return errors.As(err, &refused) && (refused.Code == apierr.Unauthenticated || refused.Code == apierr.Forbidden)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}

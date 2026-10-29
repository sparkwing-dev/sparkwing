package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestTeamAdministratorAcceptsALocalSubjectOnlyAsTheLocalPeer(t *testing.T) {
	local := func(class capability.CredentialClass, team string) capability.Principal {
		return capability.Principal{Class: class, Capability: capability.Capability{Team: team, Subject: capability.Subject{Kind: capability.SubjectLocal, ID: "local:1000"}, Audience: capability.Controller, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.TeamAdmin}}}
	}
	if !teamAdministrator(local(capability.CredentialPeer, string(DefaultTeam))) {
		t.Fatal("the verified local peer cannot administer its local team")
	}
	for name, p := range map[string]capability.Principal{
		"bearer with a local subject":  local(capability.CredentialBearer, string(DefaultTeam)),
		"session with a local subject": local(capability.CredentialSession, string(DefaultTeam)),
		"peer in another team":         local(capability.CredentialPeer, "acme"),
	} {
		if teamAdministrator(p) {
			t.Errorf("%s administers the team", name)
		}
	}
}

// A local-subject root outside the default team is never the local peer, so
// no store re-check treats it as one.
func TestLocalPeerRootIsOnlyTheDefaultTeamsRoot(t *testing.T) {
	c := capability.Capability{Team: "acme", Subject: capability.Subject{Kind: capability.SubjectLocal, ID: "local:1000"}, Audience: capability.Controller, Binding: capability.Binding{Kind: capability.BindingNone}}
	if localPeerRoot(c) {
		t.Fatal("a local-subject root in another team counts as the local peer")
	}
	c.Team = string(DefaultTeam)
	if !localPeerRoot(c) {
		t.Fatal("the default team's local root does not count as the local peer")
	}
}

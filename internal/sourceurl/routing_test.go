package sourceurl

import "testing"

func TestRoutingIdentityUsesCloneIdentity(t *testing.T) {
	for _, display := range []string{"acme/app", "https://github.com/acme/app.git/", "git@github.com:acme/app.git", "ssh://git@github.com./acme/app"} {
		identity, err := RoutingIdentity("gh:1", display)
		if err != nil || identity != "github.com/acme/app" {
			t.Fatalf("%s: identity=%s error=%v", display, identity, err)
		}
	}
}

func TestRoutingIdentityKeepsLocalAndFriendlyRepositories(t *testing.T) {
	for _, test := range []struct{ key, display, want string }{{"local:snapshot", "/home/user/project", ""}, {"git:github.com/acme/app", "My application", "github.com/acme/app"}, {"gh:42", "Friendly name", ""}} {
		got, err := RoutingIdentity(test.key, test.display)
		if err != nil || got != test.want {
			t.Fatalf("%s %s: %s %v", test.key, test.display, got, err)
		}
	}
}

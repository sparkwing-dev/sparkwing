package controller_test

import (
	"net/http"
	"net/url"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/githubauth/githubtest"
)

func TestGitHubSignInCreatesAnAccountAndItsPersonalSpace(t *testing.T) {
	f := newIdentityFixture(t)
	out := f.signInGitHub(ghPerson(101, "octo", "Octo@Example.com"))
	if out.User.Email != "octo@example.com" || out.ActiveTeam == nil || out.ActiveTeam.Slug != "octo" ||
		out.ActiveTeam.DisplayName != "octo's space" {
		t.Fatalf("github sign-in = %+v %+v", out.User, out.ActiveTeam)
	}
	var caps capabilities
	f.call("GET", "/api/v1/capabilities", "", nil, &caps)
	if !slices.Equal(caps.Auth.Providers, []string{"google", "github"}) {
		t.Fatalf("providers = %v", caps.Auth.Providers)
	}
}

func TestGitHubStartAsksOnlyForProfileAndEmails(t *testing.T) {
	f := newIdentityFixture(t)
	var start struct {
		AuthorizeURL string `json:"authorize_url"`
		State        string `json:"state"`
	}
	f.call("POST", "/api/v1/auth/oauth/github/start", "", map[string]string{"redirect_uri": dashRedirect}, &start)
	u, err := url.Parse(start.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("scope") != "read:user user:email" || q.Get("state") != start.State || q.Get("redirect_uri") != dashRedirect {
		t.Fatalf("authorize query = %v", q)
	}
	if code := f.call("POST", "/api/v1/auth/oauth/github/start", "",
		map[string]string{"redirect_uri": "https://evil.example/cb"}, nil); code != http.StatusBadRequest {
		t.Fatalf("github start with an unlisted redirect = %d, want 400", code)
	}
}

// Only the primary address GitHub has verified counts. A victim's address on
// the public profile, or verified but not primary, never signs anyone in.
func TestGitHubIgnoresUnverifiedAndProfileEmails(t *testing.T) {
	f := newIdentityFixture(t)
	victim := f.signIn(person("g-v", "victim@example.com", "Vic"))
	cases := map[string]githubtest.Person{
		"primary unverified": {ID: 201, Login: "m1", Emails: []githubtest.Email{
			{Email: "victim@example.com", Primary: true, Verified: false},
		}},
		"verified but not primary": {ID: 202, Login: "m2", Emails: []githubtest.Email{
			{Email: "mallory@example.com", Primary: true, Verified: false},
			{Email: "victim@example.com", Primary: false, Verified: true},
		}},
		"profile email only": {ID: 203, Login: "m3", ProfileEmail: "victim@example.com"},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			var out exchangeBody
			if code := f.githubExchange(p, &out); code != http.StatusForbidden {
				t.Fatalf("exchange = %d (user %s), want 403", code, out.User.ID)
			}
		})
	}
	var me meBody
	f.call("GET", "/api/v1/me", sessionAuth(victim.SessionID), nil, &me)
	if len(me.Memberships) != 1 {
		t.Fatalf("victim's memberships changed: %+v", me.Memberships)
	}
}

// The identity key is GitHub's numeric id. A renamed login keeps the account,
// and a new account that takes the old login gets nothing of it.
func TestGitHubIdentityIsKeyedOnTheNumericID(t *testing.T) {
	f := newIdentityFixture(t)
	first := f.signInGitHub(ghPerson(301, "octo", "octo@example.com"))
	renamed := f.signInGitHub(ghPerson(301, "octo-renamed", "octo@example.com"))
	if renamed.User.ID != first.User.ID {
		t.Fatal("a renamed login lost its account")
	}
	squatter := f.signInGitHub(ghPerson(302, "octo", "squatter@example.com"))
	if squatter.User.ID == first.User.ID {
		t.Fatal("a new account holding the old login signed in as the original")
	}
}

func TestGitHubLinksToAGoogleUserOnTheSameVerifiedEmail(t *testing.T) {
	f := newIdentityFixture(t)
	g := f.signIn(person("g-d", "dual@example.com", "Dual"))
	gh := f.signInGitHub(ghPerson(401, "dual", "dual@example.com"))
	if gh.User.ID != g.User.ID || gh.ActiveTeam.Slug != g.ActiveTeam.Slug {
		t.Fatalf("github on the same verified email did not join the google user: %s vs %s", gh.User.ID, g.User.ID)
	}
	other := f.signInGitHub(ghPerson(402, "dual-two", "dual@example.com"))
	if other.User.ID == g.User.ID {
		t.Fatal("a second GitHub account joined a user that already has one")
	}
}

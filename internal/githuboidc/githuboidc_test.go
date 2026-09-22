package githuboidc_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githuboidc"
	"github.com/sparkwing-dev/sparkwing/internal/githuboidc/githuboidctest"
)

const audience = "https://ci.example.com"

var job = githuboidctest.Job{Repository: "Acme/Widgets", RepositoryID: 42, RepositoryOwnerID: 7, RunID: "99"}

func TestVerifyReturnsTheJobClaims(t *testing.T) {
	iss := githuboidctest.New(t)
	got, err := githuboidc.New(iss.Config(audience)).Verify(context.Background(), iss.Token(job, audience))
	if err != nil {
		t.Fatal(err)
	}
	if got.Repository != "Acme/Widgets" || got.RepositoryID != 42 || got.RepositoryOwnerID != 7 ||
		got.RunID != "99" || got.Ref != "refs/heads/main" || got.WorkflowRef == "" {
		t.Fatalf("claims = %+v", got)
	}
}

func TestVerifyRefusesTokensThatDoNotProveTheJob(t *testing.T) {
	iss := githuboidctest.New(t)
	past := time.Now().Add(-time.Hour).Unix()
	cases := map[string]string{
		"wrong audience":   iss.Token(job, "https://other.example.com"),
		"wrong issuer":     iss.TokenWith(job, audience, map[string]any{"iss": "https://token.actions.githubusercontent.com.evil"}),
		"expired":          iss.TokenWith(job, audience, map[string]any{"exp": past}),
		"no expiry":        iss.TokenWith(job, audience, map[string]any{"exp": nil}),
		"not yet valid":    iss.TokenWith(job, audience, map[string]any{"nbf": time.Now().Add(time.Hour).Unix()}),
		"unpublished key":  iss.TokenSignedBy(iss.ForeignKey(), job, audience),
		"no repository id": iss.TokenWith(job, audience, map[string]any{"repository_id": nil}),
		"no owner id":      iss.TokenWith(job, audience, map[string]any{"repository_owner_id": "0"}),
		"bad repository":   iss.TokenWith(job, audience, map[string]any{"repository": "widgets"}),
		"not a JWT":        "abc.def",
		"alg none":         "eyJhbGciOiJub25lIn0.eyJpc3MiOiJ4In0.",
	}
	v := githuboidc.New(iss.Config(audience))
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(context.Background(), token); !errors.Is(err, githuboidc.ErrRejected) {
				t.Fatalf("Verify = %v, want ErrRejected", err)
			}
		})
	}
}

func TestVerifyRefusesWithoutAConfiguredAudience(t *testing.T) {
	iss := githuboidctest.New(t)
	cfg := iss.Config("")
	if _, err := githuboidc.New(cfg).Verify(context.Background(), iss.Token(job, "")); err == nil {
		t.Fatal("Verify accepted a token with no audience configured")
	}
}

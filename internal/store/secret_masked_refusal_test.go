package store

import "testing"

func TestMaskedSecretRefusalGivesAPasteableCommand(t *testing.T) {
	for _, tc := range []struct {
		secret Secret
		want   string
	}{
		{Secret{Name: "TOK", Masked: true, Shared: true}, "secret \"TOK\" is masked, so only a pipeline run that declares it can read its value. To store a readable, unmasked value instead, run `sparkwing secrets set --secret TOK --variable` and enter the value on stdin"},
		{Secret{Name: "DEPLOY_KEY", Masked: true, Repo: "gh:1", RepoName: "acme/app", Pipeline: "deploy"}, "secret \"DEPLOY_KEY\" is masked, so only a pipeline run that declares it can read its value. To store a readable, unmasked value instead, run `sparkwing secrets set --secret DEPLOY_KEY --repo acme/app --pipeline deploy --variable` and enter the value on stdin"},
	} {
		if got := maskedSecretRefusal(&tc.secret); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.secret.Name, got, tc.want)
		}
	}
}

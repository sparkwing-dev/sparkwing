package controller

import "testing"

func TestGitHubRunTargetURLLinksOnlyAPlainDashboard(t *testing.T) {
	for _, tc := range []struct {
		name string
		base string
		want string
	}{
		{name: "http", base: "http://sparkwing.example.com", want: "http://sparkwing.example.com/runs?run=run-1"},
		{name: "https path", base: "https://sparkwing.example.com/team/", want: "https://sparkwing.example.com/team/runs?run=run-1"},
		{name: "uppercase scheme", base: "HTTPS://sparkwing.example.com", want: "https://sparkwing.example.com/runs?run=run-1"},
		{name: "relative", base: "not-an-absolute-url"},
		{name: "hostless", base: "https:///dashboard"},
		{name: "port only", base: "https://:443"},
		{name: "scheme", base: "ftp://sparkwing.example.com"},
		{name: "credentials", base: "https://user:password@sparkwing.example.com"},
		{name: "query", base: "https://sparkwing.example.com?team=platform"},
		{name: "empty query", base: "https://sparkwing.example.com?"},
		{name: "fragment", base: "https://sparkwing.example.com#runs"},
		{name: "empty fragment", base: "https://sparkwing.example.com#"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := githubRunTargetURL(tc.base, "run-1"); got != tc.want {
				t.Errorf("target URL = %q, want %q", got, tc.want)
			}
		})
	}
}

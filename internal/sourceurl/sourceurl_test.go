package sourceurl

import "testing"

func TestValidateCloneURLAcceptsHTTPSAndGitSSH(t *testing.T) {
	cases := []string{
		"https://git.netwits.com/InevitableAI/regent.git",
		"git@github.com:sparkwing-dev/sparkwing.git",
		"ssh://git@github.com/sparkwing-dev/sparkwing.git",
	}
	for _, tc := range cases {
		t.Run(tc, func(t *testing.T) {
			got, err := ValidateCloneURL(tc)
			if err != nil {
				t.Fatalf("ValidateCloneURL: %v", err)
			}
			if got != tc {
				t.Fatalf("ValidateCloneURL = %q, want %q", got, tc)
			}
		})
	}
}

func TestValidateCloneURLRejectsUnsafeInputs(t *testing.T) {
	cases := []string{
		"file:///tmp/repo",
		"/tmp/repo",
		"https://user:secret@example.com/repo.git",
		"https://127.0.0.1/repo.git",
		"https://10.0.0.5/repo.git",
		"https://localhost/repo.git",
	}
	for _, tc := range cases {
		t.Run(tc, func(t *testing.T) {
			if _, err := ValidateCloneURL(tc); err == nil {
				t.Fatal("ValidateCloneURL error = nil, want rejection")
			}
		})
	}
}

func TestRedactStripsUserinfo(t *testing.T) {
	got := Redact("https://user:secret@example.com/repo.git")
	want := "https://redacted@example.com/repo.git"
	if got != want {
		t.Fatalf("Redact = %q, want %q", got, want)
	}
}

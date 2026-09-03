package sourceurl

import (
	"fmt"
	"testing"
)

func TestValidateCloneURLAcceptsHTTPSAndGitSSH(t *testing.T) {
	cases := []string{
		"https://git.example.com/acme/widgets.git",
		"git@github.com:sparkwing-dev/sparkwing.git",
		"ssh://git@github.com/sparkwing-dev/sparkwing.git",
		"https://git.example.com./acme/widgets.git",
		"https://8.8.8.8/acme/widgets.git",
		"https://134744072/acme/widgets.git",
		"https://100.128.0.1/acme/widgets.git",
		"https://1.2.3.4.example.com/acme/widgets.git",
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
		"https://localhost./repo.git",
		"https://LOCALHOST/repo.git",
		"https://api.localhost/repo.git",
		"https://2130706433/repo.git",
		"https://127.1/repo.git",
		"https://127.0.1/repo.git",
		"https://0x7f000001/repo.git",
		"https://0x7f.0x0.0x0.0x1/repo.git",
		"https://017700000001/repo.git",
		"https://0177.0.0.01/repo.git",
		"https://0/repo.git",
		"git@127.1:repo.git",
		"git@2130706433:repo.git",
		"https://[::1]/repo.git",
		"https://[fe80::1%25eth0]/repo.git",
		"https://169.254.169.254/repo.git",
		"https://169.254.169.254./repo.git",
		"https://metadata/repo.git",
		"https://metadata.google.internal/repo.git",
		"https://METADATA.GOOGLE.INTERNAL./repo.git",
		"https://100.100.100.200/repo.git",
		"https://100.64.0.1/repo.git",
		"https://3232235777/repo.git",
		"-upayload@example.com:repo.git",
		"--upload-pack@example.com:repo.git",
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

func TestValidateCloneURLRejectsALeadingDashInEveryComponent(t *testing.T) {
	cases := map[string]string{
		"scp host":      "a@-h:repo.git",
		"scp path":      "git@example.com:-p1234/repo",
		"ssh userinfo":  "ssh://-oProxyCommand=x@example.com/repo.git",
		"ssh host":      "ssh://git@-example.com/repo.git",
		"https host":    "https://-example.com/repo.git",
		"whole string":  "-upload-pack=x",
		"leading space": "\t--upload-pack=x",
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got, err := ValidateCloneURL(tc); err == nil {
				t.Fatalf("ValidateCloneURL(%q) = %q, want rejection", tc, got)
			}
		})
	}
}

func TestValidateCloneURLKeepsDashesThatAreNotLeading(t *testing.T) {
	cases := []string{
		"https://example.com/-repo.git",
		"https://ex-ample.com/acme/-widgets.git",
		"git@example.com:acme-corp/wid-gets.git",
		"ssh://git-bot@example.com/acme/widgets.git",
	}
	for _, tc := range cases {
		t.Run(tc, func(t *testing.T) {
			if _, err := ValidateCloneURL(tc); err != nil {
				t.Fatalf("ValidateCloneURL(%q): %v", tc, err)
			}
		})
	}
}

func TestValidateCloneURLRejectsControlCharacters(t *testing.T) {
	cases := map[string]string{
		"scp escape":     "git@example.com:repo\x1b[2Kforged.git",
		"scp nul":        "git@example.com:repo\x00.git",
		"scp vertical":   "git@example.com:repo\v.git",
		"scp delete":     "git@example.com:repo\x7f.git",
		"scp bell":       "git@example.com:repo\a.git",
		"https escape":   "https://example.com/repo\x1b.git",
		"https delete":   "https://example.com/repo\x7f.git",
		"host backspace": "git@exa\bmple.com:repo.git",
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got, err := ValidateCloneURL(tc); err == nil {
				t.Fatalf("ValidateCloneURL(%q) = %q, want rejection", tc, got)
			}
		})
	}
}

func TestValidateCloneURLRejectsAnScpHostThatHidesASecondAt(t *testing.T) {
	cases := map[string]string{
		"loopback":     "git@a@127.0.0.1:repo.git",
		"localhost":    "git@a@localhost:repo.git",
		"metadata ip":  "git@x@169.254.169.254:repo.git",
		"rfc1918":      "git@u@10.0.0.5:repo.git",
		"metadata dns": "git@u@metadata.google.internal:repo.git",
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got, err := ValidateCloneURL(tc); err == nil {
				t.Fatalf("ValidateCloneURL(%q) = %q, want rejection", tc, got)
			}
		})
	}
}

func TestValidateCloneURLRejectsInternalOnlyNamespaces(t *testing.T) {
	cases := map[string]string{
		"gcp internal": "https://db.internal/repo.git",
		"aws internal": "https://ip-10-0-0-5.us-west-2.compute.internal/repo.git",
		"mdns":         "git@buildbox.local:repo.git",
		"mdns bare":    "https://LOCAL/repo.git",
		"home network": "https://git.home.arpa/repo.git",
		"localdomain":  "https://host.localdomain/repo.git",
		"trailing dot": "https://svc.internal./repo.git",
		"scp internal": "git@svc.internal:repo.git",
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got, err := ValidateCloneURL(tc); err == nil {
				t.Fatalf("ValidateCloneURL(%q) = %q, want rejection", tc, got)
			}
		})
	}
}

func TestValidateCloneURLKeepsPublicNamesThatMerelyLookInternal(t *testing.T) {
	cases := []string{
		"https://internal.example.com/repo.git",
		"https://mylocal.example/repo.git",
		"https://local-shop.com/acme/repo.git",
		"git@internal-git.example.com:acme/repo.git",
	}
	for _, tc := range cases {
		t.Run(tc, func(t *testing.T) {
			if _, err := ValidateCloneURL(tc); err != nil {
				t.Fatalf("ValidateCloneURL(%q): %v", tc, err)
			}
		})
	}
}

func TestSetHostPolicyGatesEveryCloneHost(t *testing.T) {
	t.Cleanup(func() { SetHostPolicy(nil) })
	var seen []string
	SetHostPolicy(func(host string) error {
		seen = append(seen, host)
		if host != "git.example.com" {
			return fmt.Errorf("host %q is off the allowlist", host)
		}
		return nil
	})
	if _, err := ValidateCloneURL("https://git.example.com/acme/repo.git"); err != nil {
		t.Fatalf("allowlisted host: %v", err)
	}
	if _, err := ValidateCloneURL("git@GIT.EXAMPLE.COM.:acme/repo.git"); err != nil {
		t.Fatalf("allowlisted scp host: %v", err)
	}
	got, err := ValidateCloneURL("https://forge.elsewhere.com/acme/repo.git")
	if err == nil {
		t.Fatalf("ValidateCloneURL = %q, want the policy's rejection", got)
	}
	if err.Error() != `host "forge.elsewhere.com" is off the allowlist` {
		t.Fatalf("error = %v, want the policy's own message", err)
	}
	want := []string{"git.example.com", "git.example.com", "forge.elsewhere.com"}
	if len(seen) != len(want) {
		t.Fatalf("policy saw %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("policy saw %v, want %v", seen, want)
		}
	}

	SetHostPolicy(nil)
	if _, err := ValidateCloneURL("https://forge.elsewhere.com/acme/repo.git"); err != nil {
		t.Fatalf("after clearing the policy: %v", err)
	}
}

func TestSetHostPolicyRunsAfterTheBuiltInChecks(t *testing.T) {
	t.Cleanup(func() { SetHostPolicy(nil) })
	SetHostPolicy(func(string) error { return nil })
	for _, tc := range []string{
		"https://127.0.0.1/repo.git",
		"https://svc.internal/repo.git",
		"git@a@127.0.0.1:repo.git",
	} {
		t.Run(tc, func(t *testing.T) {
			if got, err := ValidateCloneURL(tc); err == nil {
				t.Fatalf("ValidateCloneURL(%q) = %q, want rejection", tc, got)
			}
		})
	}
}

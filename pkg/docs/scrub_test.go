package docs_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/docs"
)

// The embedded set is what the dashboard serves at /docs and what the product
// site is generated from, so every page in it is on its way to a network. This
// is the mechanical half of that check: patterns a machine can decide, run in
// the same tier as the rest of the suite. It cannot judge whether a paragraph
// reveals how someone's infrastructure is arranged -- that stays a human read.
//
// Two patterns are pinned narrower than they look. The ticket one names the
// tracker actually in use rather than a general LETTERS-DIGITS shape, which
// these pages are full of: HTTP-404, RFC-3339 and SHA-256 are not tickets. The
// sibling-tool one lists names rather than matching the family suffix, because
// sparkwing's own daemon is `sparkwing wingd`.
var scrubPatterns = []struct {
	name    string
	pattern *regexp.Regexp
	why     string
}{
	{
		"ticket identifier", regexp.MustCompile(`\bBW-\d+\b`),
		"a page that cites a ticket is not self-contained; a reader outside the tracker cannot follow it",
	},
	{
		"absolute home directory", regexp.MustCompile(`/(?:Users|home)/[a-z][a-z0-9_.-]*`),
		"names a path on somebody's machine, and usually the account name with it",
	},
	{
		"tailnet host", regexp.MustCompile(`[a-z0-9-]+\.ts\.net\b`),
		"names a host on a private network",
	},
	{
		"private hostname", regexp.MustCompile(`\b[a-z0-9-]+\.(?:internal|lan|home\.arpa)\b`),
		"names a host that only resolves inside one network",
	},
	{
		"AWS access key", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		"a credential",
	},
	{
		"GitHub token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`),
		"a credential",
	},
	{
		"API key", regexp.MustCompile(`\bsk-[A-Za-z0-9]{20,}\b`),
		"a credential",
	},
	{
		"private key block", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
		"a credential",
	},
	{
		"internal sibling tool",
		regexp.MustCompile(`\b(?:bitwing|dowing|flockwing|overwing|pulsewing|sidewing|stormwing|workwing|xwing)\b`),
		"names a tool the reader has no access to, so the page stops being self-contained",
	},
}

// knownScrubHits are the matches already in the set when this check was
// written. They are recorded rather than accepted: a new match anywhere fails
// immediately, and a recorded one that has been cleaned fails too, so the list
// can only shrink.
//
// Empty: every recorded hit has been cleaned. The map stays so any
// future deliberate exemption is recorded here rather than silently
// weakening the pattern.
var knownScrubHits = map[string][]string{}

func TestEmbeddedDocsCarryNothingPrivate(t *testing.T) {
	pages := docs.List()
	if len(pages) == 0 {
		t.Fatal("no docs embedded, so this check reads nothing")
	}
	for _, e := range pages {
		body, err := docs.ReadRaw(e.Slug)
		if err != nil {
			t.Fatalf("read %s: %v", e.Slug, err)
		}
		allowed := knownScrubHits[e.Slug]
		for _, p := range scrubPatterns {
			for _, hit := range p.pattern.FindAllString(body, -1) {
				if containsFold(allowed, hit) {
					continue
				}
				t.Errorf("docs page %q carries %q -- %s: %s", e.Slug, hit, p.name, p.why)
			}
		}
	}
}

// A recorded hit that no longer appears is a line nobody is reading, and it
// would quietly excuse the next page that happens to reproduce it.
func TestEveryRecordedScrubHitStillAppears(t *testing.T) {
	for slug, hits := range knownScrubHits {
		body, err := docs.ReadRaw(slug)
		if err != nil {
			t.Errorf("knownScrubHits names page %q, which is not in the set", slug)
			continue
		}
		for _, hit := range hits {
			if !strings.Contains(strings.ToLower(body), strings.ToLower(hit)) {
				t.Errorf("page %q no longer carries %q; drop it from knownScrubHits", slug, hit)
			}
		}
	}
}

// The set is clean apart from the recorded hits, so the check above cannot
// fail today. These cases pin what it looks for.
func TestScrubPatternsMatchWhatTheyClaimTo(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		match bool
	}{
		{"ticket id", "see BW-1234 for context", true},
		{"a version is not a ticket id", "released in v0.22.0", false},
		{"an http code is not a ticket id", "returns HTTP-404", false},
		{"a date format is not a ticket id", "timestamps are RFC-3339", false},
		{"home path", "at /Users/someone/code/x", true},
		{"linux home path", "at /home/someone/.config", true},
		{"a tilde path is fine", "at ~/.config/sparkwing", false},
		{"tailnet host", "reach it at box-1.tail1234.ts.net", true},
		{"private hostname", "point it at runner.internal", true},
		{"kubernetes service dns is public knowledge", "svc.cluster.local", false},
		{"aws key", "AKIAIOSFODNN7EXAMPLE", true},
		{"github token", "ghp_abcdefghijklmnopqrstuvwxyz0123456789", true},
		{"private key", "-----BEGIN RSA PRIVATE KEY-----", true},
		{"sibling tool", "for Makefile-style work use dowing", true},
		{"sparkwing's own daemon is not a sibling", "the sparkwing wingd daemon", false},
		{"the product's own name is not a sibling", "run sparkwing run build", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			matched := false
			for _, p := range scrubPatterns {
				if p.pattern.MatchString(tc.text) {
					matched = true
					break
				}
			}
			if matched != tc.match {
				t.Errorf("%q matched=%v, want %v", tc.text, matched, tc.match)
			}
		})
	}
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

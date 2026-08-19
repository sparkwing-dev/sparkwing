package main

import (
	"go/parser"
	"go/token"
	"regexp"
	"strings"
	"testing"
)

var runsSubmitReviewLabels = []*regexp.Regexp{
	regexp.MustCompile(`\bBLOCKER\s+[0-9]+\b`),
	regexp.MustCompile(`\bis\s+S[0-9]+(?:[.:]|\s)`),
	regexp.MustCompile(`\bhalf\s+of\s+S[0-9]+(?:[.:]|\s)`),
	regexp.MustCompile(`(?i)\badversarial review\b`),
}

func TestRunsSubmitProcessCommentsDoNotCarryReviewLabels(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "runs_submit_process_test.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range file.Comments {
		text := strings.TrimSpace(group.Text())
		for _, pattern := range runsSubmitReviewLabels {
			if pattern.MatchString(text) {
				t.Errorf("comment carries review-only label %q: %s", pattern, text)
			}
		}
	}
}

func TestRunsSubmitReviewLabelPatternsAllowProductNames(t *testing.T) {
	for _, text := range []string{
		"The fixture uses an S3 backend.",
		"The S2 protocol response is retained.",
	} {
		for _, pattern := range runsSubmitReviewLabels {
			if pattern.MatchString(text) {
				t.Errorf("pattern %q rejects durable product wording %q", pattern, text)
			}
		}
	}
}

func TestRunsSubmitReviewLabelPatternsRejectFindingNames(t *testing.T) {
	for _, text := range []string{
		"This is BLOCKER 1 end to end.",
		"This test is S3. Exit zero is correct.",
		"This is the other half of S2: the key names one intent.",
		"Regression tests lifted from adversarial review.",
	} {
		matched := false
		for _, pattern := range runsSubmitReviewLabels {
			matched = matched || pattern.MatchString(text)
		}
		if !matched {
			t.Errorf("review-only label was accepted: %q", text)
		}
	}
}

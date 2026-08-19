package main

import (
	"go/parser"
	"go/token"
	"regexp"
	"strings"
	"testing"
)

func TestRunsSubmitProcessCommentsDoNotCarryReviewLabels(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "runs_submit_process_test.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\bBLOCKER\s+[0-9]+\b`),
		regexp.MustCompile(`\bS[0-9]+\b`),
		regexp.MustCompile(`(?i)\badversarial review\b`),
	}
	for _, group := range file.Comments {
		text := strings.TrimSpace(group.Text())
		for _, pattern := range patterns {
			if pattern.MatchString(text) {
				t.Errorf("comment carries review-only label %q: %s", pattern, text)
			}
		}
	}
}

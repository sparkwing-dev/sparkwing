package main

import (
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestPrettyTextControls(t *testing.T) {
	for _, control := range []string{"\x1b[2J\x1b[H", "\r", "\x00", "\u202e", "\u2066", "\u2069", "\x7f", "\u009b", "\n", "\t", "\u061c", "\u200e", "\u200f", "\u2028", "\u2029"} {
		t.Run(control, func(t *testing.T) {
			input := "ok" + control + "forged"
			got := opsview.DisplayText(input, 160)
			if strings.ContainsFunc(got, func(r rune) bool {
				return unicode.IsControl(r) || r >= '\u202a' && r <= '\u202e' || r >= '\u2066' && r <= '\u2069'
			}) {
				t.Fatalf("active control in %q", got)
			}
			restored, err := strconv.Unquote(got)
			if err != nil || restored != input {
				t.Fatalf("control was not visibly quoted: %q (%v)", got, err)
			}
		})
	}
}

func TestPrettyTextUTF8Truncation(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"accent", strings.Repeat("é", 21), strings.Repeat("é", 18) + "..."},
		{"emoji split", strings.Repeat("a", 36) + "😀tail", strings.Repeat("a", 36) + "..."},
		{"emoji fits", strings.Repeat("a", 33) + "😀tail", strings.Repeat("a", 33) + "😀..."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := opsview.DisplayText(tc.input, 40)
			if !utf8.ValidString(got) || len(got) > 40 || got != tc.want {
				t.Fatalf("truncated %q to %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestPrettyApprovalsEscapeStoredTextAndPreserveJSON(t *testing.T) {
	input := "ok\x1b[2J\r\x00\u202eforged"
	for _, format := range []string{"pretty", "json"} {
		f := reviewFixture()
		f.approvals = controllerwire.Approvals{Approvals: []controllerwire.Approval{{RunID: "run-stable", JobID: input, Comment: input, Message: input, Approver: input}}}
		output, err := runReviewFake(t, f, []string{"runs", "approvals", "list", "-o", format})
		if err != nil {
			t.Fatal(err)
		}
		if format == "json" {
			var result controllerwire.Approval
			if err := json.Unmarshal([]byte(output), &result); err != nil {
				t.Fatal(err)
			}
			if result.Comment != input || result.Message != input || result.JobID != input {
				t.Fatalf("JSON changed stored text: %q", output)
			}
		} else if strings.ContainsAny(output, "\x1b\r\x00\u202e") {
			t.Fatalf("unsafe pretty approval: %q", output)
		}
		if f.approvals.Approvals[0].Comment != input {
			t.Fatal("rendering mutated stored text")
		}
	}
}

func TestPrettyStoredTextRenderers(t *testing.T) {
	input := "unsafe\x1b[2J\r\x00\u202e\u009b"
	detail := listedRun(input, 1)
	detail.Status, detail.Failure = "failed", input
	detail.Inputs.Pipeline, detail.Inputs.Git.Ref, detail.Inputs.Agent = input, input, input
	detail.Inputs.Lineage.RetryOf, detail.Inputs.Lineage.RetryReason = input, input
	detail.Jobs = []controllerwire.JobDetail{{ID: input, Outcome: "failed", StatusDetail: input, Error: input}}
	detail.Steps = []controllerwire.StepDetail{{ID: input, JobID: input, Outcome: "failed", Reason: input}}
	detail.Attempts = []controllerwire.AttemptDetail{{JobID: input}}
	for name, render := range map[string]func(*strings.Builder) error{
		"run list": func(out *strings.Builder) error { return writeListedRun(out, detail, "pretty", false) },
		"run detail": func(out *strings.Builder) error {
			return writeRunDetail(out, detail, runStatusOptions{format: "pretty"})
		},
		"run timeline": func(out *strings.Builder) error {
			return writeRunDetail(out, detail, runStatusOptions{format: "pretty", view: "timeline"})
		},
		"run errors": func(out *strings.Builder) error { return writeRunErrors(out, detail, "pretty") },
		"run tree":   func(out *strings.Builder) error { writeRunTree(out, &runTree{Run: detail}, "", false); return nil },
		"receipt": func(out *strings.Builder) error {
			return writeRunReceipt(out, controllerwire.Receipt{RunID: input, Pipeline: input, Steps: []controllerwire.ReceiptStep{{JobID: input, ID: input}}}, "pretty")
		},
		"failure group": func(out *strings.Builder) error {
			return renderRunFailures(t.Context(), listingReader{}, out, []controllerwire.RunDetail{detail}, runListOptions{format: "pretty", group: "job"})
		},
		"pipeline pivot": func(out *strings.Builder) error {
			return renderPipelineList(t.Context(), listingReader{}, out, io.Discard, controllerwire.RunPage{Runs: []controllerwire.RunDetail{detail}}, runListOptions{format: "pretty"})
		},
		"logs": func(out *strings.Builder) error {
			return writeLogRecord(out, controllerwire.LogRecord{RunID: input, JobID: input, Line: input}, "pretty", false)
		},
		"log event": func(out *strings.Builder) error {
			return writeLogRecord(out, controllerwire.LogRecord{RunID: input, Event: "message", Line: strconv.Quote(input)}, "pretty", false)
		},
		"schedule": func(out *strings.Builder) error {
			return renderScheduleRecord(out, scheduleSummary{Name: input, Owner: input, LockRef: input}, "", "pretty")
		},
		"queue": func(out *strings.Builder) error {
			return renderQueue(out, wingwire.QueueState{Holders: []wingwire.Holder{{RunID: input, Pipeline: input, Repo: input}}, Waiters: []wingwire.Waiter{{RunID: input, Pipeline: input, Repo: input, BlockingReason: input}}}, "pretty")
		},
		"artifact": func(out *strings.Builder) error {
			return writeArtifact(out, artifactRecord{JobID: input, ManifestEntry: blob.ManifestEntry{Path: input}}, "pretty")
		},
	} {
		t.Run(name, func(t *testing.T) {
			var out strings.Builder
			if err := render(&out); err != nil {
				t.Fatal(err)
			}
			if strings.ContainsAny(out.String(), "\x1b\r\x00\u202e\u009b") {
				t.Fatalf("active controls: %q", out.String())
			}
			if !strings.Contains(out.String(), `\`) {
				t.Fatalf("stored text lost: %q", out.String())
			}
		})
	}
}

func TestPrettyLogsKeepPlainAndJSONValues(t *testing.T) {
	input := "ok\x1b[2J\r\x00\u202eforged"
	record := controllerwire.LogRecord{RunID: "run", JobID: "job", Line: input}
	for _, format := range []string{"plain", "json"} {
		var out strings.Builder
		if err := writeLogRecord(&out, record, format, false); err != nil {
			t.Fatal(err)
		}
		if format == "plain" {
			if out.String() != input+"\n" {
				t.Fatalf("plain log changed: %q", out.String())
			}
		} else {
			var got controllerwire.LogRecord
			if err := json.Unmarshal([]byte(out.String()), &got); err != nil || got.Line != input {
				t.Fatalf("JSON log changed: %q (%v)", out.String(), err)
			}
		}
	}
}

func TestPrettyAnnotationsEscapeStoredText(t *testing.T) {
	f := reviewFixture()
	input := "ok\x1b[2J\r\x00\u202eforged"
	f.run.Jobs[0].ID = input
	f.run.Jobs[0].Annotations[0].Message = input
	f.run.Jobs[0].Annotations[0].File = input
	for _, format := range []string{"pretty", "json"} {
		out, err := runReviewFake(t, f, []string{"runs", "annotations", "list", "--run", "412", "-o", format})
		if err != nil {
			t.Fatal(err)
		}
		if format == "pretty" {
			if strings.ContainsAny(out, "\x1b\r\x00\u202e") {
				t.Fatalf("unsafe annotation: %q", out)
			}
		} else {
			entries := decodeNDJSON[annotationEntry](t, out)
			if entries[0].Message != input || entries[0].File != input {
				t.Fatalf("JSON annotation changed: %q", out)
			}
		}
	}
}

func TestPrettyTextPreservesPrintableUnicodeAndHandlesSmallLimits(t *testing.T) {
	for _, input := range []string{"café", "👩‍💻", "normal text"} {
		if got := opsview.DisplayText(input); got != input {
			t.Fatalf("printable text changed: %q", got)
		}
	}
	for limit := 1; limit <= 4; limit++ {
		got := opsview.DisplayText("😀suffix", limit)
		if len(got) > limit || !utf8.ValidString(got) {
			t.Fatalf("limit %d produced %q", limit, got)
		}
	}
	if got := opsview.DisplayText("bad\xff"); !utf8.ValidString(got) || strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("invalid source bytes were not escaped: %q", got)
	}
}

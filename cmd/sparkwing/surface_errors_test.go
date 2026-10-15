package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
)

func TestSurfaceErrorsUseSharedEnvelope(t *testing.T) {
	inv, parseErr := clisurface.Parse("sparkwing", []string{"runs", "status", "--run=r1", "--output=json", "--unknown=private-value"})
	var out bytes.Buffer
	calls := 0
	encode := func(w io.Writer, failure *apierr.Error) error {
		calls++
		return json.NewEncoder(w).Encode(apierr.Envelope{Error: failure})
	}
	if err := writeInvocationError(&out, &invocationError{inv: inv, cause: parseErr}, encode); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || strings.Count(out.String(), "\n") != 1 || strings.Contains(out.String(), "private-value") {
		t.Fatalf("diagnostic: %s", &out)
	}
	var envelope apierr.Envelope
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil || envelope.Error.Code != apierr.BadRequest {
		t.Fatalf("envelope: %s %v", &out, err)
	}
}

func TestSurfaceErrorsPreserveStructuredHintAndCode(t *testing.T) {
	want := &apierr.Error{Code: apierr.LockHeld, Message: "claim expired", Hint: "claim again", Details: map[string]any{"reason": "lease"}}
	for _, format := range []string{"plain", "pretty", "json"} {
		inv, err := clisurface.Parse("sparkwing", []string{"runs", "status", "--run=r1", "-o", format})
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		encode := func(w io.Writer, got *apierr.Error) error {
			if got != want {
				t.Fatalf("structured error was replaced: %#v", got)
			}
			return json.NewEncoder(w).Encode(apierr.Envelope{Error: got})
		}
		if err := writeInvocationError(&out, &invocationError{inv: inv, cause: want}, encode); err != nil {
			t.Fatal(err)
		}
		if format != "json" && out.String() != "sparkwing: runs status: claim expired\nhint: claim again\n" {
			t.Fatalf("diagnostic: %q", out.String())
		}
	}
}

func TestSurfaceErrorExitCodes(t *testing.T) {
	for _, code := range []int{1, 2, 3} {
		err := &invocationError{cause: exitError(code, errors.New("failed"))}
		if exitCodeFor(err) != code {
			t.Fatalf("exit %d became %d", code, exitCodeFor(err))
		}
	}
}

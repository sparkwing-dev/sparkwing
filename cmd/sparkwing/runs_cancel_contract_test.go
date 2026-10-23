package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

type cancellationController struct {
	controllerwire.Controller
	calls []string
}

func (c *cancellationController) Cancel(_ context.Context, id string, request controllerwire.CancelRequest) (controllerwire.Applied, error) {
	c.calls = append(c.calls, id)
	if request.Reason != "" {
		return controllerwire.Applied{}, errors.New("invented cancellation reason")
	}
	if id == "missing" {
		return controllerwire.Applied{}, &apierr.Error{Code: apierr.NotFound, Message: "run absent", Hint: "check the run number"}
	}
	return controllerwire.Applied{StateSeq: 12, Generation: 4, Attempt: 2, CancelRequested: true, Replayed: id == "finished"}, nil
}

func TestCancelUsesOneControllerAndPreservesResults(t *testing.T) {
	c := &cancellationController{}
	var out bytes.Buffer
	err := cancelRunIDs(t.Context(), c, []string{"412", "missing", "finished"}, &out, "json")
	if err == nil || !reflect.DeepEqual(c.calls, []string{"412", "missing", "finished"}) {
		t.Fatalf("calls=%v err=%v", c.calls, err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("records: %s", &out)
	}
	for i, line := range lines {
		var record cancellationResult
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			if record.Error == nil || record.Error.Code != apierr.NotFound || record.Error.Hint == "" {
				t.Fatalf("error lost: %#v", record)
			}
		} else if record.Applied == nil || record.Applied.StateSeq != 12 || record.Applied.Generation != 4 || record.Applied.Replayed != (i == 2) {
			t.Fatalf("applied result lost: %#v", record)
		}
	}
}

func TestCancelPlainListsSuccessfulRunIDs(t *testing.T) {
	var out bytes.Buffer
	if err := cancelRunIDs(t.Context(), &cancellationController{}, []string{"412", "finished"}, &out, "plain"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "412\nfinished\n" {
		t.Fatalf("plain result: %q", out.String())
	}
}

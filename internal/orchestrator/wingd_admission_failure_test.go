package orchestrator

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var admitFailOnce sync.Once

func registerAdmitFailPipeline() {
	admitFailOnce.Do(func() {
		sparkwing.Register[sparkwing.NoInputs]("admit-fail-e2e",
			func() sparkwing.Pipeline[sparkwing.NoInputs] { return &admitFailE2EPipe{} })
	})
}

type admitFailE2EPipe struct{ sparkwing.Base }

func (p *admitFailE2EPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "step", func(_ context.Context) error { return nil })
	return nil
}

type admitFailSink struct {
	mu      sync.Mutex
	records []sparkwing.LogRecord
}

func (s *admitFailSink) Log(level, msg string) {
	s.Emit(sparkwing.LogRecord{Level: level, Msg: msg})
}

func (s *admitFailSink) Emit(rec sparkwing.LogRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, rec)
}

func (s *admitFailSink) snapshot() []sparkwing.LogRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]sparkwing.LogRecord, len(s.records))
	copy(out, s.records)
	return out
}

func TestRun_AdmissionFailureEmitsDelegateFinish(t *testing.T) {
	registerAdmitFailPipeline()
	home := wingdTestHome(t)
	backends, _, _ := openWingdBackends(t, home)

	dl := &admitFailSink{}
	admission := &LocalAdmission{
		Home:        home,
		Version:     "test",
		Out:         io.Discard,
		Spawn:       func(string, string) error { return errors.New("no daemon for test") },
		DialTimeout: 100 * time.Millisecond,
		Backoff:     10 * time.Millisecond,
	}

	res, err := Run(context.Background(), backends, Options{
		Pipeline:  "admit-fail-e2e",
		RunID:     "admit-fail-test-1",
		Delegate:  dl,
		Admission: admission,
	})
	if err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}

	var finishRec *sparkwing.LogRecord
	for _, rec := range dl.snapshot() {
		if rec.Event == "run_finish" && rec.Level == "error" {
			r := rec
			finishRec = &r
			break
		}
	}
	if finishRec == nil {
		t.Fatal("delegate received no run_finish error event after admission failure")
	}
	errStr, _ := finishRec.Attrs["error"].(string)
	if errStr == "" {
		t.Fatalf("run_finish event missing error attr; attrs = %v", finishRec.Attrs)
	}
}

// TestAdmissionFailure_NeverAdmissibleCarriesTheDaemonsArithmetic proves the
// refusal an operator reads names what was asked for against what exists. The
// old wording blamed a concurrency group for every never-admissible answer,
// including the host-capacity ones, which sent the reader to look at limits
// that had nothing to do with it.
func TestAdmissionFailure_NeverAdmissibleCarriesTheDaemonsArithmetic(t *testing.T) {
	err := admissionFailure(&wingdclient.AdmissionError{
		Key:    "never_admissible",
		Policy: wingwire.PolicyFail,
		Reason: "needs 12GiB of memory, this machine has 8GiB",
	})
	want := "local admission: this request can never be admitted on this box: needs 12GiB of memory, this machine has 8GiB"
	if err.Error() != want {
		t.Errorf("refusal = %q, want %q", err.Error(), want)
	}

	bare := admissionFailure(&wingdclient.AdmissionError{Key: "never_admissible", Policy: wingwire.PolicyFail})
	if !strings.Contains(bare.Error(), "concurrency group") {
		t.Errorf("reasonless refusal = %q, want the concurrency-group fallback", bare.Error())
	}
}

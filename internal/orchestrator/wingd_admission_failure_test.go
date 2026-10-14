package orchestrator

import (
	"strings"
	"sync"
	"testing"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

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

func TestAdmissionFailure_NeverAdmissibleCarriesTheDaemonsArithmetic(t *testing.T) {
	err := admissionFailure(nil, &wingdclient.AdmissionError{
		Key:    "never_admissible",
		Policy: wingwire.PolicyFail,
		Reason: "needs 12GiB of memory, this machine has 8GiB",
	})
	want := "local admission: this request can never be admitted on this box: needs 12GiB of memory, this machine has 8GiB"
	if err.Error() != want {
		t.Errorf("refusal = %q, want %q", err.Error(), want)
	}

	bare := admissionFailure(nil, &wingdclient.AdmissionError{Key: "never_admissible", Policy: wingwire.PolicyFail})
	if !strings.Contains(bare.Error(), "concurrency group") {
		t.Errorf("reasonless refusal = %q, want the concurrency-group fallback", bare.Error())
	}
}

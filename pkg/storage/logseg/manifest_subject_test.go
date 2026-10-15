package logseg_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
)

func TestOpenRejectsManifestForAnotherSubject(t *testing.T) {
	subject := jobSubject()
	prefix, err := logseg.SubjectPrefix(logseg.ClassStandard, subject)
	if err != nil {
		t.Fatal(err)
	}
	for name, other := range map[string]blob.LogSubject{
		"other job":   {Kind: blob.LogJobAttempt, Team: subject.Team, RunID: subject.RunID, Attempt: subject.Attempt},
		"owner unit":  ownerSubject(blob.LogPlan),
		"invalid tag": {Kind: blob.LogOwnerUnit, Team: subject.Team, RunID: subject.RunID, Attempt: subject.Attempt},
	} {
		t.Run(name, func(t *testing.T) {
			if name == "other job" {
				attempt := *other.Attempt
				attempt.NodeID = "test"
				other.Attempt = &attempt
			}
			raw, err := json.Marshal(logseg.Manifest{Version: logseg.ManifestVersion, Subject: &other, Status: logseg.StatusComplete})
			if err != nil {
				t.Fatal(err)
			}
			objects := newMemObjects()
			objects.put(logseg.ManifestKey(prefix), raw)
			if _, err := logseg.Open(t.Context(), objects, prefix); !errors.Is(err, logseg.ErrBadManifest) {
				t.Fatalf("foreign manifest opened: %v", err)
			}
		})
	}
}

func TestParseManifestRequiresTaggedSubject(t *testing.T) {
	for _, raw := range []string{
		`{"version":1,"status":"complete"}`,
		`{"version":1,"status":"complete","subject":null}`,
		`{"version":1,"status":"complete","run":"run-1","node":"build","attempt":1,"generation":1}`,
		`{"version":1,"status":"complete","subject":{"kind":"owner_unit","team":"acme","run_id":"run-1"}}`,
	} {
		if _, err := logseg.ParseManifest([]byte(raw)); !errors.Is(err, logseg.ErrBadManifest) {
			t.Errorf("untagged manifest accepted: %s: %v", raw, err)
		}
	}
}

func TestAbandonedSealCannotChangeSubject(t *testing.T) {
	prefix, err := logseg.SubjectPrefix(logseg.ClassStandard, jobSubject())
	if err != nil {
		t.Fatal(err)
	}
	objects := newMemObjects()
	_, err = logseg.SealAbandoned(t.Context(), objects, prefix,
		logseg.Manifest{Subject: new(ownerSubject(blob.LogPlan))}, logseg.ReasonRunnerLost, objects.putIfAbsent)
	if !errors.Is(err, logseg.ErrBadManifest) {
		t.Fatalf("abandoned seal changed subject: %v", err)
	}
	if keys, err := objects.List(t.Context(), prefix); err != nil || len(keys) != 0 {
		t.Fatalf("refused seal wrote objects: %v, %v", keys, err)
	}
}

func TestSinkCopiesItsSubject(t *testing.T) {
	for _, subject := range []blob.LogSubject{jobSubject(), ownerSubject(blob.LogEval)} {
		want := jobSubject()
		if subject.Kind == blob.LogOwnerUnit {
			want = ownerSubject(blob.LogEval)
		}
		prefix, err := logseg.SubjectPrefix(logseg.ClassStandard, want)
		if err != nil {
			t.Fatal(err)
		}
		objects := newMemObjects()
		sink := newSink(t, newMemUploader(objects, prefix), logseg.Options{Subject: subject})
		if subject.Attempt != nil {
			subject.Attempt.Generation++
		} else {
			subject.Unit.Generation++
			subject.Unit.Source.Generation++
		}
		m, err := sink.Close(t.Context(), logseg.End{})
		if err != nil || m.Subject == nil || !reflect.DeepEqual(*m.Subject, want) {
			t.Fatalf("sink changed subject: %+v, %v", m, err)
		}
		if _, err := logseg.Open(t.Context(), objects, prefix); err != nil {
			t.Fatal(err)
		}
	}
}

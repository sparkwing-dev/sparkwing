package logseg_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
)

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

func TestSinkCopiesItsSubject(t *testing.T) {
	for _, subject := range []blob.LogSubject{jobSubject(), ownerSubject(blob.LogEval)} {
		want := jobSubject()
		if subject.Kind == blob.LogOwnerUnit {
			want = ownerSubject(blob.LogEval)
		}
		prefix, err := blob.LogSubjectPrefix(logseg.ClassStandard, want)
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
		if raw, err := m.Marshal(); err != nil {
			t.Fatal(err)
		} else if _, err := logseg.ParseManifest(raw); err != nil {
			t.Fatal(err)
		}
	}
}

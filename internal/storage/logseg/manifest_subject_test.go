package logseg

import (
	"errors"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func TestParseManifestRequiresTaggedSubject(t *testing.T) {
	for _, raw := range []string{
		`{"version":1,"status":"complete"}`,
		`{"version":1,"status":"complete","subject":null}`,
		`{"version":1,"status":"complete","run":"run-1","node":"build","attempt":1,"generation":1}`,
		`{"version":1,"status":"complete","subject":{"kind":"owner_unit","team":"acme","run_id":"run-1"}}`,
	} {
		if _, err := parseManifest([]byte(raw)); !errors.Is(err, errBadManifest) {
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
		prefix, err := blob.LogSubjectPrefix(ClassStandard, want)
		if err != nil {
			t.Fatal(err)
		}
		objects := newMemObjects()
		sink := newSink(t, newMemUploader(objects, prefix), Options{Subject: subject})
		if subject.Attempt != nil {
			subject.Attempt.Generation++
		} else {
			subject.Unit.Generation++
			subject.Unit.Source.Generation++
		}
		m, err := sink.Close(t.Context(), End{})
		if err != nil || m.Subject == nil || !reflect.DeepEqual(*m.Subject, want) {
			t.Fatalf("sink changed subject: %+v, %v", m, err)
		}
		if raw, err := m.Marshal(); err != nil {
			t.Fatal(err)
		} else if _, err := parseManifest(raw); err != nil {
			t.Fatal(err)
		}
	}
}

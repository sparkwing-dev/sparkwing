package logseg

import (
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

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
		if _, err := m.Marshal(); err != nil {
			t.Fatal(err)
		}
	}
}

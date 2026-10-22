package sparkwing

import "testing"

func TestAnnotateEmitsLocation(t *testing.T) {
	recorder := &recordingEmitter{}
	ctx := withTestNode(withTestLogger(t.Context(), recorder), "ingest")
	Annotate(ctx, Annotation{Level: "warn", Message: "check this", File: "src/main.go", Line: 12, Column: 3})
	if len(recorder.records) != 1 {
		t.Fatalf("annotations = %+v", recorder.records)
	}
	record := recorder.records[0]
	if record.Event != "annotation" || record.Level != "warn" || record.Msg != "check this" || record.Attrs["file"] != "src/main.go" || record.Attrs["line"] != 12 || record.Attrs["column"] != 3 {
		t.Fatalf("annotation = %+v", record)
	}
}

package nodeproto

import "testing"

func TestAnnotationValidateNamesTheFieldAndTheAllowedLevels(t *testing.T) {
	for annotation, want := range map[Annotation]string{
		{Level: "warning"}:       `annotation level "warning" is not one of debug, info, warn, error`,
		{Column: -1}:             "annotation column -1 is negative",
		{Line: 5, EndLine: 4}:    "annotation end_line 4 is before line 5",
		{Level: "warn", Line: 1}: "",
	} {
		err := annotation.Validate()
		if got := ""; err != nil {
			got = err.Error()
			if got != want {
				t.Errorf("%+v: %q, want %q", annotation, got, want)
			}
		} else if want != "" {
			t.Errorf("%+v accepted, want %q", annotation, want)
		}
	}
}

package controllerwire

import (
	"encoding/json"
	"reflect"
	"testing"
)

func FuzzLocalScheduleEnvironmentRoundTrip(f *testing.F) {
	f.Add([]byte("TOKEN=private\xff\x80\n"))
	f.Add([]byte("PATH=/bin"))
	f.Fuzz(func(t *testing.T, entry []byte) {
		input := LocalScheduleRegistration{Mode: ScheduleUpsert, RepoPath: "/checkout", Environment: []string{string(entry)}}
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		var actual LocalScheduleRegistration
		if err := json.Unmarshal(data, &actual); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(input.Environment, actual.Environment) {
			t.Fatal("transport changed captured environment bytes")
		}
	})
}

func TestLocalScheduleRegistrationRejectsUnknownFields(t *testing.T) {
	var result LocalScheduleRegistration
	if json.Unmarshal([]byte(`{"mode":"replace","unexpected":true}`), &result) == nil {
		t.Fatal("unknown field accepted")
	}
}

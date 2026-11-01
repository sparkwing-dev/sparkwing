package controller

import (
	"encoding/json"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"testing"
)

func TestWarmCapacityConsoleCommitsAndRevocation(t *testing.T) {
	s, raw, csrf := reviewAccount(t, "reader")
	path := "/api/v1/operator/warm-capacity"
	reviewSessionRequest(t, s, raw, csrf, "GET", path, "", 403)
	s.WithOperatorAccounts([]string{"member"})
	read := reviewSessionRequest(t, s, raw, csrf, "GET", path, "", 200)
	var before store.WarmCapacity
	if err := json.Unmarshal(read.Body.Bytes(), &before); err != nil || len(before.Classes) != 6 {
		t.Fatalf("capacity=%s err=%v", read.Body, err)
	}
	for _, c := range before.Classes {
		if c.Mode != "off" || c.Desired != 0 || c.StartTimeTargetSeconds != 10 {
			t.Fatalf("default=%+v", c)
		}
	}
	for _, body := range []string{
		`{"class":"sparkwing-2cpu","mode":"manual","manual_count":3,"auto_min":0,"auto_max":0,"start_time_target_seconds":10}`,
		`{"class":"sparkwing-2cpu","mode":"auto","manual_count":0,"auto_min":1,"auto_max":4,"start_time_target_seconds":0.5}`,
		`{"class":"sparkwing-2cpu","mode":"off","manual_count":0,"auto_min":0,"auto_max":0,"start_time_target_seconds":10}`,
	} {
		written := reviewSessionRequest(t, s, raw, csrf, "PUT", path, body, 200)
		var state store.WarmCapacity
		if err := json.Unmarshal(written.Body.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		var want store.WarmCapacitySettings
		if err := json.Unmarshal([]byte(body), &want); err != nil {
			t.Fatal(err)
		}
		if state.Classes[0].WarmCapacitySettings != want {
			t.Fatalf("settings=%+v want=%+v", state.Classes[0], want)
		}
	}
	for _, body := range []string{
		`{}`, `{"class":"unknown","mode":"off","start_time_target_seconds":10}`,
		`{"class":"sparkwing-2cpu","mode":"auto","auto_min":2,"auto_max":1,"start_time_target_seconds":10}`,
		`{"class":"sparkwing-2cpu","mode":"manual","manual_count":-1,"start_time_target_seconds":10}`,
		`{"class":"sparkwing-2cpu","mode":"off","start_time_target_seconds":0}`,
		`{"class":"sparkwing-2cpu","mode":"off","start_time_target_seconds":10,"actor":"forged"}`,
	} {
		reviewSessionRequest(t, s, raw, csrf, "PUT", path, body, 400)
	}
	body := `{"class":"sparkwing-2cpu","mode":"manual","manual_count":9,"start_time_target_seconds":10}`
	reviewSessionRequest(t, s, raw, "", "PUT", path, body, 403)
	s.WithOperatorAccounts(nil)
	reviewSessionRequest(t, s, raw, csrf, "PUT", path, body, 403)
	after, err := s.store.AsOperator().WarmCapacity(t.Context())
	if err != nil || after.Classes[0].Mode != "off" {
		t.Fatalf("revoked write persisted %+v %v", after, err)
	}
}

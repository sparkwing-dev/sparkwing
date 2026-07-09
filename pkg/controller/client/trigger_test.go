package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func TestCreateTrigger_RetriesWithoutHostAdmissionForOlderController(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		requests = append(requests, body)
		w.Header().Set("Content-Type", "application/json")
		if len(requests) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"json: unknown field \"host_admission\""}`)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"run_id":"child","status":"pending"}`)
	}))
	defer server.Close()

	c := client.New(server.URL, nil)
	resp, err := c.CreateTrigger(context.Background(), client.TriggerRequest{
		Pipeline: "child",
		PlanAdmission: client.TriggerPlanAdmission{
			Key:              "g:plan",
			HolderID:         "parent/-",
			Admissions:       map[string]string{"g:plan": "parent/-"},
			HostAdmission:    true,
			HostAdmissionKey: "g:plan",
		},
	})
	if err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	if resp.RunID != "child" {
		t.Fatalf("run id = %q, want child", resp.RunID)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want retry", len(requests))
	}
	retryAdmission, ok := requests[1]["plan_admission"].(map[string]any)
	if !ok {
		t.Fatalf("retry plan_admission = %#v", requests[1]["plan_admission"])
	}
	if _, ok := retryAdmission["host_admission"]; ok {
		t.Fatalf("retry still sent host_admission: %#v", retryAdmission)
	}
	if _, ok := retryAdmission["host_admission_key"]; ok {
		t.Fatalf("retry still sent host_admission_key: %#v", retryAdmission)
	}
	if retryAdmission["key"] != "g:plan" || retryAdmission["holder_id"] != "parent/-" {
		t.Fatalf("retry admission = %#v, want key and holder preserved", retryAdmission)
	}
}

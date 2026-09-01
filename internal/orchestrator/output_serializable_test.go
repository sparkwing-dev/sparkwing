package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type serializableOK struct {
	Name  string            `json:"name"`
	Count int               `json:"count"`
	Tags  []string          `json:"tags"`
	Meta  map[string]string `json:"meta"`
	When  time.Time         `json:"when"`
	Inner *serializableOK   `json:"inner,omitempty"`
}

type withChannel struct {
	Name string    `json:"name"`
	Done chan bool `json:"done"`
}

type withFunc struct {
	Fn func() error `json:"fn"`
}

type allUnexported struct {
	name  string
	count int
}

type withOpaqueDeps struct {
	Name string `json:"name"`
	Mu   sync.Mutex
	File *os.File
	Buf  bytes.Buffer
}

type withSkippedChannel struct {
	Name string    `json:"name"`
	Done chan bool `json:"-"`
}

func TestValidateOutputSerializable_Accepts(t *testing.T) {
	ok := []reflect.Type{
		reflect.TypeOf(serializableOK{}),
		reflect.TypeOf(&serializableOK{}),
		reflect.TypeOf([]serializableOK{}),
		reflect.TypeOf(map[string]serializableOK{}),
		reflect.TypeOf(""),
		reflect.TypeOf(0),

		reflect.TypeOf(time.Time{}),

		reflect.TypeOf(withSkippedChannel{}),

		reflect.TypeOf(allUnexported{}),
		reflect.TypeOf(withOpaqueDeps{}),
		reflect.TypeOf(sync.Mutex{}),
		reflect.TypeOf(&os.File{}),
	}
	for _, tp := range ok {
		if err := validateOutputSerializable(tp); err != nil {
			t.Errorf("validateOutputSerializable(%s) = %v, want nil", tp, err)
		}
	}
}

func TestValidateOutputSerializable_Rejects(t *testing.T) {
	cases := []struct {
		typ  reflect.Type
		want string
	}{
		{reflect.TypeOf(withChannel{}), "channel"},
		{reflect.TypeOf(withFunc{}), "func"},
		{reflect.TypeOf(complex128(0)), "complex"},
		{reflect.TypeOf([]withChannel{}), "channel"},
		{reflect.TypeOf(map[string]withFunc{}), "func"},
	}
	for _, tc := range cases {
		err := validateOutputSerializable(tc.typ)
		if err == nil {
			t.Errorf("validateOutputSerializable(%s) = nil, want a rejection", tc.typ)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("validateOutputSerializable(%s) = %q, want it to mention %q", tc.typ, err, tc.want)
		}
	}
}

type unencodableJob struct {
	sparkwing.Base
	sparkwing.Produces[withChannel]
}

func (j *unencodableJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(context.Context) (withChannel, error) {
		return withChannel{Name: "x", Done: make(chan bool)}, nil
	}), nil
}

type encodableJob struct {
	sparkwing.Base
	sparkwing.Produces[serializableOK]
}

func (j *encodableJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(context.Context) (serializableOK, error) {
		return serializableOK{Name: "x"}, nil
	}), nil
}

func TestPlanOutputTypeErrors(t *testing.T) {
	clean := sparkwing.NewPlan()
	sparkwing.Job(clean, "ok", &encodableJob{})
	if err := planOutputTypeErrors(clean); err != nil {
		t.Fatalf("clean plan rejected: %v", err)
	}

	bad := sparkwing.NewPlan()
	sparkwing.Job(bad, "broken", &unencodableJob{})
	err := planOutputTypeErrors(bad)
	if err == nil {
		t.Fatal("plan with an unencodable job output was accepted")
	}
	for _, want := range []string{"broken", "channel", processBoundaryGuide} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestPlanOutputTypeErrors_CoversRecoveryNodes(t *testing.T) {
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "ok", &encodableJob{}).OnFailure("recover", &unencodableJob{})
	err := planOutputTypeErrors(plan)
	if err == nil {
		t.Fatal("unencodable recovery-node output was accepted")
	}
	if !strings.Contains(err.Error(), "recover") {
		t.Errorf("error %q does not name the recovery node", err)
	}
}

func TestNodeOutputMarshalError_CitesTheGuide(t *testing.T) {
	err := nodeOutputMarshalError("build", withChannel{}, context.Canceled)
	for _, want := range []string{"build", "orchestrator.withChannel", processBoundaryGuide} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestValidateOutputSerializable_AcceptedTypesActuallyMarshal(t *testing.T) {
	values := []any{
		serializableOK{Name: "n", Count: 1},
		allUnexported{name: "n", count: 1},
		withOpaqueDeps{Name: "n"},
		withSkippedChannel{Name: "n", Done: make(chan bool)},
		time.Now(),
	}
	for _, v := range values {
		tp := reflect.TypeOf(v)
		if err := validateOutputSerializable(tp); err != nil {
			t.Errorf("validateOutputSerializable(%s) = %v, want nil", tp, err)
			continue
		}
		if _, err := json.Marshal(v); err != nil {
			t.Errorf("accepted %s but json.Marshal rejects it: %v", tp, err)
		}
	}
}

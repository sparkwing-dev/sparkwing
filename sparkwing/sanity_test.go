package sparkwing

import (
	"context"
	"encoding/json"
	"testing"
)

type sanityBuildOut struct {
	Tag    string
	Digest string
}

type buildJob struct {
	Base
	Produces[sanityBuildOut]
}

func (b *buildJob) Work(work *Work) (*WorkStep, error) {
	return Step(work, "run", func(context.Context) (sanityBuildOut, error) {
		return sanityBuildOut{Tag: "v1", Digest: "sha256:abc"}, nil
	}), nil
}

func jobFnNoop() func(context.Context) error {
	return func(context.Context) error { return nil }
}

type recordingLogger struct{ lines []string }

func (r *recordingLogger) Log(level, message string) { r.lines = append(r.lines, level+": "+message) }
func (r *recordingLogger) Emit(record LogRecord) {
	r.lines = append(r.lines, record.Level+": "+record.Msg)
}

type recordingEmitter struct{ records []LogRecord }

func (r *recordingEmitter) Log(level, message string) {
	r.Emit(LogRecord{Level: level, Msg: message})
}
func (r *recordingEmitter) Emit(record LogRecord) { r.records = append(r.records, record) }

func TestPlanJobAndNeeds(t *testing.T) {
	plan := NewPlan()
	build := Job(plan, "build", &buildJob{})
	deploy := Job(plan, "deploy", jobFnNoop()).Needs(build)
	if len(plan.nodes) != 2 || plan.byID["deploy"] != deploy || len(deploy.needs) != 1 || deploy.needs[0] != "build" {
		t.Fatalf("plan graph = %+v", planDocument(plan).Jobs)
	}
	if build.outType == nil || RefTo[sanityBuildOut](build).Job() != "build" {
		t.Fatal("typed result was not registered")
	}
}

func TestStaticGroupMembership(t *testing.T) {
	plan := NewPlan()
	a := Job(plan, "a", jobFnNoop())
	b := Job(plan, "b", jobFnNoop())
	group := GroupJobs(plan, "checks", a, b)
	join := Job(plan, "join", jobFnNoop()).Needs(group)
	if len(join.needs) != 2 || join.needs[0] != "a" || join.needs[1] != "b" {
		t.Fatalf("group dependency = %v", join.needs)
	}
	if groups := jobGroups(plan, a); len(groups) != 1 || groups[0] != "checks" {
		t.Fatalf("group names = %v", groups)
	}
}

func TestLoggerUsesContext(t *testing.T) {
	logger := &recordingLogger{}
	Info(withTestLogger(t.Context(), logger), "hi %s", "there")
	if len(logger.lines) != 1 || logger.lines[0] != "info: hi there" {
		t.Fatalf("log = %v", logger.lines)
	}
}

func TestRefResolvesFromContext(t *testing.T) {
	want := sanityBuildOut{Tag: "v2", Digest: "sha256:def"}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	ctx := withTestJSONResolver(t.Context(), func(_ context.Context, id string) ([]byte, error) {
		if id != "build" {
			return nil, ErrRefAbsent
		}
		return raw, nil
	})
	if got := (Ref[sanityBuildOut]{JobID: "build"}).Get(ctx); got != want {
		t.Fatalf("reference = %+v, want %+v", got, want)
	}
}

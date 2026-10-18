package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

type installedScheduleSet struct {
	controllerwire.ScheduleRepositoryManager
	rows map[string]controllerwire.ScheduleDeclaration
}

func (s *installedScheduleSet) WriteSchedules(_ context.Context, input controllerwire.ScheduleRepoRequest) (controllerwire.ScheduleRepoWritten, error) {
	if input.Mode == controllerwire.ScheduleReplace {
		s.rows = map[string]controllerwire.ScheduleDeclaration{}
	}
	for _, row := range input.Schedules {
		s.rows[row.Pipeline+"/"+row.Name] = row
	}
	return controllerwire.ScheduleRepoWritten{}, nil
}

func TestSelectiveScheduleInstallPreservesUnselectedDeclarations(t *testing.T) {
	for _, selector := range []string{"sweep/cloud", "sweep/host"} {
		t.Run(selector, func(t *testing.T) {
			f := newScheduleCommandFixture(t)
			owner := &installedScheduleSet{rows: map[string]controllerwire.ScheduleDeclaration{
				"sweep/cloud": {Pipeline: "sweep", Name: "cloud", Cron: "0 1 * * *"},
				"sweep/other": {Pipeline: "sweep", Name: "other", Cron: "0 2 * * *"},
			}}
			f.command.load = func(string) (*projectconfig.Config, error) {
				return projectconfig.Parse([]byte(scheduleProject + "        - name: other\n          cron: '0 3 * * *'\n          where: controller\n"))
			}
			f.command.connect = func(context.Context, string) (scheduleConnection, error) {
				return scheduleConnection{repositories: owner, close: func() {}}, nil
			}
			var out bytes.Buffer
			err := runScheduleCommandArgs(t.Context(), []string{"install", "--profile=prod", "--schedule=" + selector, "-o=json"}, &out, f.command)
			if err != nil {
				t.Fatal(err)
			}
			if len(owner.rows) != 2 || owner.rows["sweep/other"].Cron != "0 2 * * *" {
				t.Fatalf("selection changed unrelated schedules: %+v", owner.rows)
			}
			want := "0 4 * * *"
			if strings.HasSuffix(selector, "/host") {
				want = "0 1 * * *"
			}
			if owner.rows["sweep/cloud"].Cron != want {
				t.Fatalf("selected cadence = %q, want %q", owner.rows["sweep/cloud"].Cron, want)
			}
		})
	}
}

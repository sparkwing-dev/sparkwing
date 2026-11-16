package projectconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScheduleErrorsGiveTheParserReasonAndNameTheFileOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), Filename)
	for cron, want := range map[string]string{
		"61 * * * *": `pipelines[0].on.schedule[0].cron: line 2: minute field: value 61 out of range 0-59 in "61"; example: "0 * * * *" (hourly)`,
		"0 25 * * *": `pipelines[0].on.schedule[0].cron: line 2: hour field: value 25 out of range 0-23 in "25"; example: "0 * * * *" (hourly)`,
		"@nightly":   `pipelines[0].on.schedule[0].cron: line 2: unknown alias "@nightly"; use @hourly, @daily, @weekly, @monthly, @yearly or five fields; example: "0 * * * *" (hourly)`,
	} {
		raw := "# pipelines\npipelines: [{name: build, entrypoint: Build, on: {schedule: [{name: nightly, cron: '" + cron + "', where: local}]}}]\n"
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if err == nil || err.Error() != path+": "+want {
			t.Errorf("%s: error %v, want %q", cron, err, path+": "+want)
		}
		if err != nil && strings.Count(err.Error(), Filename) != 1 {
			t.Errorf("%s: file named more than once: %v", cron, err)
		}
	}
	raw := "pipelines: [{name: build, entrypoint: Build, on: {schedule: [{name: nightly, cron: '0 3 * * *', where: local, tz: America/Denvr}]}}]\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.HasSuffix(err.Error(), `unknown time zone "America/Denvr"; use an IANA name such as America/Denver, or local`) {
		t.Errorf("time zone error = %v", err)
	}
}

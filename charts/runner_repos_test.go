package charts

import (
	"io"
	"slices"
	"strconv"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runners/runnerflags"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

func TestRunnerChartsPreserveRepositoryAdmission(t *testing.T) {
	for _, full := range []bool{false, true} {
		chart, template, prefix := "./sparkwing-runner-bundle", "templates/runner-deployment.yaml", ""
		if full {
			chart, template, prefix = "./sparkwing-full", "charts/sparkwing-runner-bundle/templates/runner-deployment.yaml", "sparkwing-runner-bundle."
		}
		t.Run(chart, func(t *testing.T) {
			for _, repos := range [][]string{nil, {"github.com/acme/*", "git.example.com/team/app"}} {
				var settings []string
				for i, repo := range repos {
					settings = append(settings, prefix+"runner.repos["+strconv.Itoa(i)+"]="+repo)
				}
				rendered := helmRender(t, chart, template, "sparkwing", settings...)
				flags, err := runnerflags.Parse("warm", runnerContainer(t, rendered).Args[1:], io.Discard)
				if err != nil {
					t.Fatal(err)
				}
				patterns, err := flags.GetStringSlice("repos")
				if err != nil || !slices.Equal(patterns, repos) {
					t.Fatalf("repository flags = %v, want %v: %v", patterns, repos, err)
				}
				allowed, err := sourceurl.ParseRepoAllowlist(patterns)
				if err != nil || allowed.Admits("github.com/acme/app") != (len(repos) > 0) || allowed.Admits("github.com/foreign/app") {
					t.Fatalf("repository admission = %v: %v", allowed, err)
				}
			}
		})
	}
}

package jobs

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type WeatherOut struct {
	Forecast string    `json:"forecast"`
	TempF    int       `json:"temp_f"`
	At       time.Time `json:"at"`
}

type WeatherReport struct{ sparkwing.Base }

func (WeatherReport) ShortHelp() string {
	return "Toy pipeline: emits a synthetic weather reading (used by `example`)"
}

func (WeatherReport) Help() string {
	return "A standalone pipeline that returns a synthetic WeatherOut from its `stamp` node. The `example` pipeline calls it via sparkwing.RunAndAwait to exercise the cross-pipeline dependency surface. Runs in well under a second; the random forecast varies per invocation so the dashboard shows a fresh value each spawn."
}

func (WeatherReport) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Trigger the toy pipeline directly", Command: "sparkwing run weather-report"},
	}
}

type weatherStamp struct {
	sparkwing.Base
	sparkwing.Produces[WeatherOut]
}

func (j *weatherStamp) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", j.run), nil
}

func (j *weatherStamp) run(ctx context.Context) (WeatherOut, error) {
	if err := chatter(ctx, 400, []string{
		"querying NOAA endpoint",
		"parsing response",
		"computing local forecast",
	}); err != nil {
		return WeatherOut{}, err
	}
	forecasts := []string{"sunny", "partly cloudy", "overcast", "light rain", "thunderstorms"}
	out := WeatherOut{
		Forecast: forecasts[rand.IntN(len(forecasts))],
		TempF:    55 + rand.IntN(35),
		At:       time.Now(),
	}
	sparkwing.Annotate(ctx, fmt.Sprintf("forecast: %s · %d°F", out.Forecast, out.TempF))
	return out, nil
}

func (p *WeatherReport) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "stamp", &weatherStamp{}).Inline()
	return nil
}

func init() {
	sparkwing.Register("weather-report", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &WeatherReport{} })
}

package main

import (
	"context"
	"encoding/base64"
	"errors"
	"os"

	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

type (
	pipeline    struct{ sw.Base }
	credentials struct {
		Token string `sw:"TOKEN,required"`
	}
)

func (pipeline) Secrets() any { return &credentials{} }
func (pipeline) Plan(_ context.Context, p *sw.Plan, _ sw.NoInputs, _ sw.RunContext) error {
	sw.Job(p, "job", func(ctx context.Context) error {
		if _, found := os.LookupEnv("TOKEN"); found || os.Getenv("PATH") == "" {
			return errors.New("requester secret entered process environment or host PATH is missing")
		}
		value := sw.PipelineSecrets[credentials](ctx).Token
		if value == "" || sw.Runner(ctx) == nil || sw.Runner(ctx).Name == "" {
			return errors.New("missing requester secret or granted runner")
		}
		body, err := os.ReadFile("source-marker.txt")
		if err != nil || string(body) != "uploaded edited source\n" {
			return errors.New("executor did not use the uploaded source")
		}
		if _, err := os.Stat(".env"); !os.IsNotExist(err) {
			return errors.New("secret file reached the executor")
		}
		sw.Info(ctx, "secret=%s encoded=%s", value, base64.StdEncoding.EncodeToString([]byte(value)))
		if err := os.MkdirAll("modules", 0o700); err != nil {
			return err
		}
		if err := os.WriteFile("modules/dependency", []byte("cached burst dependency\n"), 0o600); err != nil {
			return err
		}
		return os.WriteFile("burst-result.txt", []byte("artifact from burst SDK\n"), 0o640)
	}).Requires("worker").Artifacts("burst-result.txt").Cache(sw.Dir("modules", sw.KeyFromFile("source-marker.txt")))
	return nil
}

func main() {
	sw.Register("burst-execution-proof", func() sw.Pipeline[sw.NoInputs] { return pipeline{} })
	sw.Main()
}

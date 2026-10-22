package main

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

type inputs struct {
	Text     string        `flag:"text" required:"true" enum:" yes , no "`
	Bool     bool          `flag:"bool"`
	Int      int           `flag:"int"`
	Int64    int64         `flag:"int64"`
	Float    float64       `flag:"float"`
	Duration time.Duration `flag:"duration"`
	List     []string      `flag:"list" enum:" dev , prod " default:"dev, prod"`
	Empty    string        `flag:"empty" default:""`
}
type requiredDefault struct {
	Value string `flag:"value" required:"true" default:"bad"`
}
type enumType struct {
	Value int `flag:"value" enum:"1" required:"true"`
}
type enumAnchor struct {
	Value string `flag:"value" enum:"one"`
}
type enumDefault struct {
	Value string `flag:"value" enum:"one" default:"two"`
}
type floatDefault struct {
	Value float64 `flag:"value" default:"NaN"`
}
type duplicateName struct {
	One string `flag:"value"`
	Two string `flag:"value"`
}
type duplicateShort struct {
	One string `flag:"one" short:"v"`
	Two string `flag:"two" short:"v"`
}
type invalidShort struct {
	One string `flag:"one" short:"long"`
}
type requiredEmpty struct {
	Value string `flag:"value" required:"true" default:""`
}
type pipe[T any] struct{}

func (pipe[T]) Plan(_ context.Context, p *sw.Plan, _ T, _ sw.RunContext) error {
	sw.Job(p, "job", func(context.Context) error { return nil })
	return nil
}

type generatedJob[T any] struct{ sw.WithArgs[T] }

func (*generatedJob[T]) Work(w *sw.Work) (*sw.WorkStep, error) {
	return sw.Step(w, "run", func(context.Context) error { return nil }), nil
}

type generatedPipe[T any] struct{}

type conditionalInputs struct {
	Enable bool `flag:"enable"`
}

type conditionalSecret struct {
	Token string `flag:"token" secret:"true" default:"conditional-secret-default"`
}

type conditionalPipe struct{}

func (conditionalPipe) Plan(_ context.Context, p *sw.Plan, args conditionalInputs, _ sw.RunContext) error {
	sw.Job(p, "source", func(context.Context) error { return nil })
	if args.Enable {
		sw.Job(p, "conditional", &generatedJob[conditionalSecret]{})
	}
	return nil
}

func (generatedPipe[T]) Plan(_ context.Context, p *sw.Plan, _ sw.NoInputs, _ sw.RunContext) error {
	sw.Job(p, "source", func(context.Context) error { return nil }).Generate(func(_ context.Context, expansion *sw.Plan) error {
		sw.Job(expansion, "child", &generatedJob[T]{})
		return nil
	})
	return nil
}

func register[T any](name string) {
	if len(os.Args) > 1 {
		sw.Register[sw.NoInputs](name, func() sw.Pipeline[sw.NoInputs] { return generatedPipe[T]{} })
	} else {
		sw.Register[T](name, func() sw.Pipeline[T] { return pipe[T]{} })
	}
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--conditional" {
		sw.Register[conditionalInputs]("conditional", func() sw.Pipeline[conditionalInputs] { return conditionalPipe{} })
		doc, err := sdkserve.DefaultOperations.Describe(context.Background())
		if err != nil {
			panic(err)
		}
		_, plan, err := sdkserve.DefaultOperations.Plan(context.Background(), "conditional", map[string]string{"enable": "true"}, nodeproto.RunContext{Pipeline: "conditional"})
		if err != nil {
			panic(err)
		}
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"description": doc, "plan": plan}); err != nil {
			panic(err)
		}
		return
	}
	register[inputs]("args")
	register[requiredDefault]("required-default")
	register[enumType]("enum-type")
	register[enumAnchor]("enum-anchor")
	register[enumDefault]("enum-default")
	register[floatDefault]("float-default")
	register[duplicateName]("duplicate-name")
	register[duplicateShort]("duplicate-short")
	register[invalidShort]("invalid-short")
	register[requiredEmpty]("required-empty")
	if len(os.Args) > 1 {
		jobs := map[string][]nodeproto.JobSpec{}
		for _, name := range []string{"args", "required-default", "enum-type", "enum-anchor", "enum-default", "float-default", "duplicate-name", "duplicate-short", "invalid-short", "required-empty"} {
			built, _, err := sdkserve.DefaultOperations.Plan(context.Background(), name, nil, nodeproto.RunContext{Pipeline: name})
			if err != nil {
				panic(err)
			}
			result, err := sdkserve.DefaultOperations.Eval(context.Background(), built, nodeproto.Request{Closure: "source/generate/0", Input: json.RawMessage("null")})
			if err != nil || result.Error != nil {
				panic(result.Error)
			}
			var members []nodeproto.JobSpec
			if err := json.Unmarshal(result.Value, &members); err != nil {
				panic(err)
			}
			jobs[name] = members
		}
		if err := json.NewEncoder(os.Stdout).Encode(jobs); err != nil {
			panic(err)
		}
		return
	}
	doc, err := sdkserve.DefaultOperations.Describe(context.Background())
	if err != nil {
		panic(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(doc); err != nil {
		panic(err)
	}
}

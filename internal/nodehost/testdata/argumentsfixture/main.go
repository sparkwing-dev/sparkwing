package main

import (
	"context"
	"encoding/json"
	"os"
	"time"

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

func main() {
	sw.Register[inputs]("args", func() sw.Pipeline[inputs] { return pipe[inputs]{} })
	sw.Register[requiredDefault]("required-default", func() sw.Pipeline[requiredDefault] { return pipe[requiredDefault]{} })
	sw.Register[enumType]("enum-type", func() sw.Pipeline[enumType] { return pipe[enumType]{} })
	sw.Register[enumAnchor]("enum-anchor", func() sw.Pipeline[enumAnchor] { return pipe[enumAnchor]{} })
	sw.Register[enumDefault]("enum-default", func() sw.Pipeline[enumDefault] { return pipe[enumDefault]{} })
	sw.Register[floatDefault]("float-default", func() sw.Pipeline[floatDefault] { return pipe[floatDefault]{} })
	sw.Register[duplicateName]("duplicate-name", func() sw.Pipeline[duplicateName] { return pipe[duplicateName]{} })
	sw.Register[duplicateShort]("duplicate-short", func() sw.Pipeline[duplicateShort] { return pipe[duplicateShort]{} })
	sw.Register[invalidShort]("invalid-short", func() sw.Pipeline[invalidShort] { return pipe[invalidShort]{} })
	sw.Register[requiredEmpty]("required-empty", func() sw.Pipeline[requiredEmpty] { return pipe[requiredEmpty]{} })
	doc, err := sdkserve.DefaultOperations.Describe(context.Background())
	if err != nil {
		panic(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(doc); err != nil {
		panic(err)
	}
}

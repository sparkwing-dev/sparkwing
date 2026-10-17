package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"os"
	"sync"

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
		return os.WriteFile("burst-result.txt", []byte("artifact from burst SDK\n"), 0o640)
	}).Artifacts("burst-result.txt")
	return nil
}

type pipeTransport struct {
	mu       sync.Mutex
	requests *os.File
	reader   *bufio.Reader
}

func (p *pipeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := errors.Join(request.Write(p.requests), request.Body.Close()); err != nil {
		return nil, err
	}
	response, err := http.ReadResponse(p.reader, request)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(response.Body)
	err = errors.Join(err, response.Body.Close())
	response.Body = io.NopCloser(bytes.NewReader(body))
	return response, err
}

func main() {
	requests, err := os.OpenFile(".burst-requests", os.O_WRONLY, 0)
	if err != nil {
		panic(err)
	}
	defer requests.Close()
	responses, err := os.Open(".burst-responses")
	if err != nil {
		panic(err)
	}
	defer responses.Close()
	http.DefaultTransport = &pipeTransport{requests: requests, reader: bufio.NewReader(responses)}
	sw.Register("burst-execution-proof", func() sw.Pipeline[sw.NoInputs] { return pipeline{} })
	sw.Main()
}

package client_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func ExampleNewRunReader() {
	httpClient := &http.Client{Transport: exampleTransport(func(*http.Request) (*http.Response, error) {
		body, err := json.Marshal(controllerwire.RunPage{Runs: []controllerwire.RunDetail{{ID: "run-1", Inputs: runspec.Display{Pipeline: "build"}, Status: "success"}}})
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})}
	c, err := client.NewRunReader(client.EngineOptions{URL: "https://controller.example", HTTP: httpClient})
	if err != nil {
		fmt.Println("client:", err)
		return
	}
	page, err := c.ListRuns(context.Background(), controllerwire.RunQuery{Limit: 20})
	if err != nil {
		fmt.Println("list:", err)
		return
	}
	for _, r := range page.Runs {
		fmt.Printf("%s %s %s\n", r.ID, r.Inputs.Pipeline, r.Status)
	}
	// Output: run-1 build success
}

type exampleTransport func(*http.Request) (*http.Response, error)

func (f exampleTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

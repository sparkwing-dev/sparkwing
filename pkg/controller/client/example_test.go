package client_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func ExampleNewRunReader() {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"runs":[{"id":"run-1","inputs":{"pipeline":"build"},"status":"success"}]}`)
	}))
	defer mock.Close()

	c, err := client.NewRunReader(client.EngineOptions{URL: mock.URL})
	if err != nil {
		fmt.Println("client:", err)
		return
	}
	page, err := c.ListRuns(context.Background(), controllerwire.RunQuery{})
	if err != nil {
		fmt.Println("list:", err)
		return
	}
	for _, r := range page.Runs {
		fmt.Printf("%s %s %s\n", r.ID, r.Inputs.Pipeline, r.Status)
	}
	// Output: run-1 build success
}

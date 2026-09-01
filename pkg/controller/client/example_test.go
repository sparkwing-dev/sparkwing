package client_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func ExampleClient_ListRuns() {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"runs":[{"id":"run-1","pipeline":"build","status":"success"}]}`)
	}))
	defer mock.Close()

	c := client.New(mock.URL, nil)
	runs, err := c.ListRuns(context.Background(), store.RunFilter{})
	if err != nil {
		fmt.Println("list:", err)
		return
	}
	for _, r := range runs {
		fmt.Printf("%s %s %s\n", r.ID, r.Pipeline, r.Status)
	}
	// Output: run-1 build success
}

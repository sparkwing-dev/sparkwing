// Command genoutputfixture speaks the node protocol by hand for the tests of
// generators that read their source's output through the node routes, as an
// SDK generator does. GENFIXTURE_SCENARIO picks the plan: recovery generates
// a child that fails and its recovery node; multi has a second generator,
// which returns nothing, and a summary that needs both; nested gives the child
// a generator of its own. With GENFIXTURE_PHASE=omitted the first generator
// returns nothing.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func readOutput(routes nodeproto.ProcessRoutes, node string) error {
	req, err := http.NewRequest(http.MethodGet, routes.URL+"/node/v1/nodes/"+node+"/output", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+routes.Bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("read %s output: %d %s", node, resp.StatusCode, body)
	}
	return nil
}

func main() {
	var routes nodeproto.ProcessRoutes
	scenario, phase := os.Getenv("GENFIXTURE_SCENARIO"), os.Getenv("GENFIXTURE_PHASE")
	generators := []string{"targets/generate/0"}
	if scenario == "multi" {
		generators = append(generators, "targets/generate/1")
	}
	source := map[string]any{"id": "targets", "steps": []any{map[string]any{"id": "body"}}, "result_step": "body", "deps": []string{}, "closures": map[string]any{"generate": generators}, "timeout_ms": 5000}
	child := map[string]any{"id": "child", "steps": []any{map[string]any{"id": "body"}}, "result_step": "body", "groups": []string{"targets/generate/0"}, "deps": []string{"targets"}, "timeout_ms": 5000}
	recover := map[string]any{"id": "recover", "steps": []any{map[string]any{"id": "body"}}, "result_step": "body", "groups": []string{"targets/generate/0"}, "deps": []string{}, "on_failure_of": "child", "timeout_ms": 5000}
	if scenario == "nested" {
		child["closures"] = map[string]any{"generate": []string{"child/generate/0"}}
	}
	summary := map[string]any{"id": "summary", "steps": []any{map[string]any{"id": "body"}}, "result_step": "body", "deps": []string{}, "needs_generated": generators}
	reqs := bufio.NewScanner(os.NewFile(3, "requests"))
	reqs.Buffer(make([]byte, 1<<20), 1<<20)
	frames := os.NewFile(4, "frames")
	for reqs.Scan() {
		var q struct {
			ID      any                       `json:"id"`
			Op      string                    `json:"op"`
			Closure string                    `json:"closure"`
			Job     string                    `json:"job"`
			Binding *nodeproto.ProcessBinding `json:"binding"`
			Run     struct {
				RunID string `json:"run_id"`
			} `json:"run"`
		}
		if err := json.Unmarshal(reqs.Bytes(), &q); err != nil {
			panic(err)
		}
		var result any = map[string]any{}
		switch q.Op {
		case "hello":
			if q.Binding != nil {
				routes = q.Binding.Routes
			}
			result = nodeproto.HelloResult{Protocol: nodeproto.Version, SDK: nodeproto.SDKIdentity{Language: "go", Version: "test"}, Features: []string{}}
		case "describe":
			result = map[string]any{"pipelines": []any{map[string]any{"name": "probe", "args": []any{}}}}
		case "plan":
			result = map[string]any{"jobs": []any{source, summary}}
		case "eval":
			var value []any
			var err error
			switch q.Closure {
			case "targets/generate/0":
				err = readOutput(routes, "targets")
				switch {
				case phase == "omitted":
					value = []any{}
				case scenario == "recovery":
					value = []any{child, recover}
				default:
					value = []any{child}
				}
			case "child/generate/0":
				err = readOutput(routes, "child")
				value = []any{map[string]any{"id": "grandchild", "steps": []any{map[string]any{"id": "body"}}, "result_step": "body", "groups": []string{"child/generate/0"}, "deps": []string{"child"}, "timeout_ms": 5000}}
			default:
				value = []any{}
			}
			if err != nil {
				result = map[string]any{"error": &apierr.Error{Code: apierr.SDKError, Message: err.Error()}}
			} else {
				result = map[string]any{"value": value}
			}
		case "run_step":
			switch {
			case q.Job == "targets" || (q.Job == "child" && scenario == "nested"):
				result = map[string]any{"outcome": "success", "output": []string{"child"}}
			case q.Job == "child":
				result = map[string]any{"outcome": "failed", "error": &apierr.Error{Code: apierr.SDKError, Message: "the child failed"}}
			default:
				result = map[string]any{"outcome": "success"}
			}
		}
		line, err := json.Marshal(map[string]any{"reply": q.ID, "ok": true, "result": result})
		if err != nil {
			panic(err)
		}
		if _, err := frames.Write(append(line, '\n')); err != nil {
			return
		}
	}
}

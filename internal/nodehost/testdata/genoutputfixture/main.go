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
)

func readOutput(node string) error {
	req, err := http.NewRequest(http.MethodGet, os.Getenv("SPARKWING_NODE_URL")+"/node/v1/nodes/"+node+"/output", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Sparkwing-Node-Protocol", "1")
	req.Header.Set("Authorization", "Bearer "+os.Getenv("SPARKWING_NODE_TOKEN"))
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
	scenario, phase := os.Getenv("GENFIXTURE_SCENARIO"), os.Getenv("GENFIXTURE_PHASE")
	generators := []string{"targets/generator/0"}
	if scenario == "multi" {
		generators = append(generators, "targets/generator/1")
	}
	source := map[string]any{"id": "targets", "deps": []string{}, "closures": map[string]any{"generator": generators}, "modifiers": map[string]any{"timeout_ms": 5000}}
	child := map[string]any{"id": "child", "deps": []string{"targets"}, "modifiers": map[string]any{"timeout_ms": 5000}}
	recover := map[string]any{"id": "recover", "deps": []string{}, "on_failure_of": "child", "modifiers": map[string]any{"timeout_ms": 5000}}
	if scenario == "nested" {
		child["closures"] = map[string]any{"generator": []string{"child/generator/0"}}
	}
	summary := map[string]any{"id": "summary", "deps": []string{}, "needs_generated": generators}
	reqs := bufio.NewScanner(os.NewFile(3, "requests"))
	reqs.Buffer(make([]byte, 1<<20), 1<<20)
	frames := os.NewFile(4, "frames")
	for reqs.Scan() {
		var q struct {
			ID      any    `json:"id"`
			Op      string `json:"op"`
			Closure string `json:"closure"`
			Node    string `json:"node"`
			Run     struct {
				RunID string `json:"run_id"`
			} `json:"run"`
		}
		if err := json.Unmarshal(reqs.Bytes(), &q); err != nil {
			panic(err)
		}
		var result any = map[string]any{}
		switch q.Op {
		case "describe":
			result = map[string]any{"protocol": "1", "pipelines": []any{map[string]any{"name": "probe", "args": []any{}}}}
		case "plan":
			result = map[string]any{"protocol": "1", "pipeline": "probe", "run_id": q.Run.RunID, "nodes": []any{source, summary}}
		case "eval":
			var value []any
			var err error
			switch q.Closure {
			case "targets/generator/0":
				err = readOutput("targets")
				switch {
				case phase == "omitted":
					value = []any{}
				case scenario == "recovery":
					value = []any{child, recover}
				default:
					value = []any{child}
				}
			case "child/generator/0":
				err = readOutput("child")
				value = []any{map[string]any{"id": "grandchild", "deps": []string{"child"}, "modifiers": map[string]any{"timeout_ms": 5000}}}
			default:
				value = []any{}
			}
			if err != nil {
				result = map[string]any{"error": err.Error()}
			} else {
				result = map[string]any{"value": value}
			}
		case "run_node", "run_step":
			switch {
			case q.Node == "targets" || (q.Node == "child" && scenario == "nested"):
				result = map[string]any{"outcome": "success", "output": []string{"child"}}
			case q.Node == "child":
				result = map[string]any{"outcome": "failed", "error": "the child failed"}
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

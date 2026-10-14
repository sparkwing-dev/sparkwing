// Command genfixture speaks the node protocol by hand for the generated-node
// tests: its plan holds one source whose generator returns a child and the
// child's recovery node, and in mode nested the child's own generator returns
// a grandchild. Each eval appends its closure id to GENFIXTURE_TRACE, and in
// mode hang an eval never answers.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	mode, trace := os.Getenv("GENFIXTURE_MODE"), os.Getenv("GENFIXTURE_TRACE")
	source := map[string]any{"id": "targets", "deps": []string{}, "closures": map[string]any{"generator": []string{"targets/generator/0"}}, "modifiers": map[string]any{"timeout_ms": 20}}
	child := map[string]any{"id": "child", "deps": []string{"targets"}, "modifiers": map[string]any{"timeout_ms": 20}}
	handler := map[string]any{"id": "recover", "deps": []string{}, "on_failure_of": "child"}
	if mode == "nested" {
		child["closures"] = map[string]any{"generator": []string{"child/generator/0"}}
	}
	reqs := bufio.NewScanner(os.NewFile(3, "requests"))
	reqs.Buffer(make([]byte, 1<<20), 1<<20)
	frames := os.NewFile(4, "frames")
	for reqs.Scan() {
		var q struct {
			ID      any    `json:"id"`
			Op      string `json:"op"`
			Closure string `json:"closure"`
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
			result = map[string]any{"protocol": "1", "pipeline": "probe", "run_id": q.Run.RunID, "nodes": []any{source}}
		case "eval":
			if trace != "" {
				if f, err := os.OpenFile(trace, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
					_, _ = fmt.Fprintln(f, q.Closure)
					_ = f.Close()
				}
			}
			if mode == "hang" {
				continue
			}
			value := []any{child, handler}
			if q.Closure != "targets/generator/0" {
				value = []any{map[string]any{"id": "grandchild", "deps": []string{"child"}}}
			}
			result = map[string]any{"value": value}
		case "run_node", "run_step":
			result = map[string]any{"outcome": "success"}
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

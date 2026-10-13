//go:build unix

package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/protofd"
)

var update = flag.Bool("update", false, "rewrite the golden transcripts under testdata/transcripts")

type fixtureFile struct {
	Fixture struct {
		Forged string `json:"forged"`
	} `json:"fixture"`
	Run      map[string]any `json:"run"`
	Sessions []session      `json:"sessions"`
}

type session struct {
	Name         string `json:"name"`
	PrintsForged bool   `json:"prints_forged"`
	SamePlanAs   string `json:"same_plan_as"`
	Requests     []step `json:"requests"`
}

type step struct {
	Send      map[string]any   `json:"send"`
	Malformed bool             `json:"malformed"`
	Expect    expect           `json:"expect"`
	Parallel  []step           `json:"parallel"`
	Logs      []map[string]any `json:"logs"`
}

type expect struct {
	Code            string           `json:"code"`
	MessageContains string           `json:"message_contains"`
	Result          map[string]any   `json:"result"`
	Absent          []string         `json:"absent"`
	PlanNodes       map[string]any   `json:"plan_nodes"`
	Logs            []map[string]any `json:"logs"`
}

type schemas struct{ proto, doc, record *Schema }

// safety: the host writes these from what it drives, so an SDK record that
// carries one would duplicate or contradict the host's.
var hostEvents = []string{"step_start", "step_end", "step_skipped", "work_fail_fast", "node_start", "node_end", "retry"}

func TestGoSDKConforms(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "gofixture")
	build := exec.Command("go", "build", "-o", bin, "./gofixture")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the Go fixture: %v\n%s", err, out)
	}
	runSuite(t, "go", []string{bin})
}

// safety: sessions run in file order because same_plan_as compares a plan
// with one an earlier session recorded.
func runSuite(t *testing.T, sdk string, command []string) {
	raw, err := os.ReadFile(filepath.Join("testdata", "fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx fixtureFile
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("testdata/fixture.json: %v", err)
	}
	var sc schemas
	for dst, name := range map[**Schema]string{&sc.proto: "node-protocol", &sc.doc: "describe", &sc.record: "log-record"} {
		if *dst, err = LoadSchema(filepath.Join("..", "..", "..", "docs", "schemas", name+".schema.json")); err != nil {
			t.Fatal(err)
		}
	}
	plans := map[string]any{}
	for _, sess := range fx.Sessions {
		t.Run(sess.Name, func(t *testing.T) {
			p := start(t, command, sc)
			for _, st := range sess.Requests {
				p.do(st, fx.Run)
			}
			p.end()
			if sess.PrintsForged && (!strings.Contains(p.stdout.String(), fx.Fixture.Forged) || !strings.Contains(p.stderr.String(), fx.Fixture.Forged)) {
				t.Errorf("stdout %q and stderr %q, want the fixture's forged line verbatim in both", p.stdout.String(), p.stderr.String())
			}
			plans[sess.Name] = p.plan
			if want, ok := plans[sess.SamePlanAs]; sess.SamePlanAs != "" && (!ok || !reflect.DeepEqual(want, p.plan)) {
				t.Errorf("plan differs from session %s's for the same request; plan code must be deterministic", sess.SamePlanAs)
			}
			golden(t, filepath.Join("testdata", "transcripts", sdk, sess.Name+".txt"), p.transcript)
		})
	}
}

type proc struct {
	t              *testing.T
	sc             schemas
	cmd            *exec.Cmd
	req            *os.File
	frames         *nodeproto.Reader
	stdout, stderr bytes.Buffer
	tracker        nodeproto.Tracker
	transcript     []string
	plan           map[string]any
}

// safety: the process gets what a host gives an unbound process: exactly
// --sw-node-protocol, the pipes on descriptors 3 and 4, stdin from the null
// device and no SPARKWING_* variable but the handle list it must remove.
func start(t *testing.T, command []string, sc schemas) *proc {
	reqR, reqW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	frR, frW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)
	p := &proc{t: t, sc: sc, req: reqW, frames: nodeproto.NewReader(frR)}
	p.cmd = exec.CommandContext(ctx, command[0], append(slices.Clone(command[1:]), protofd.Arg)...)
	p.cmd.Dir = t.TempDir()
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "SPARKWING_") {
			p.cmd.Env = append(p.cmd.Env, kv)
		}
	}
	p.cmd.Env = append(p.cmd.Env, protofd.HandlesEnv+"=3,4", "GORACE=atexit_sleep_ms=0")
	p.cmd.ExtraFiles = []*os.File{reqR, frW}
	p.cmd.Stdout, p.cmd.Stderr = &p.stdout, &p.stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = reqR.Close()
	_ = frW.Close()
	t.Cleanup(func() {
		_ = reqW.Close()
		_ = frR.Close()
		if p.cmd.ProcessState == nil {
			_ = p.cmd.Process.Kill()
			_ = p.cmd.Wait()
		}
	})
	return p
}

func (p *proc) do(st step, run map[string]any) {
	p.t.Helper()
	group := st.Parallel
	if group == nil {
		group = []step{st}
	}
	ids := make([]string, len(group))
	for i, g := range group {
		ids[i] = p.send(g, run)
	}
	mark := len(p.transcript)
	replies, logs := p.await(ids)
	if len(group) > 1 {
		slices.Sort(p.transcript[mark:])
	}
	for i, g := range group {
		p.check(g.Send, replies[ids[i]], logs, g.Expect)
	}
	for _, want := range st.Logs {
		requireLog(p.t, logs, want)
	}
}

func (p *proc) send(st step, run map[string]any) string {
	p.t.Helper()
	req := map[string]any{}
	for k, v := range st.Send {
		req[k] = v
	}
	if req["op"] == "plan" && req["run"] == nil {
		req["run"] = run
	}
	if ref, ok := req["closure"].(map[string]any); ok {
		req["closure"] = p.closureID(ref)
	}
	op, _ := req["op"].(string)
	req["id"] = p.tracker.Start(nodeproto.Op(op))
	line, err := json.Marshal(req)
	if err != nil {
		p.t.Fatal(err)
	}
	if !st.Malformed {
		p.validate(p.sc.proto, "request", req, "request")
	}
	p.transcript = append(p.transcript, "-> "+string(line))
	if _, err := p.req.Write(append(line, '\n')); err != nil {
		p.t.Fatalf("write request: %v", err)
	}
	return req["id"].(string)
}

// safety: every frame is judged as the host judges it, by ParseFrame and the
// tracker, and then against docs/schemas, so a frame the host would take as
// protocol_error fails the session even when its values look right.
func (p *proc) await(ids []string) (map[string]map[string]any, []map[string]any) {
	p.t.Helper()
	replies := map[string]map[string]any{}
	var logs []map[string]any
	for len(replies) < len(ids) {
		line, err := p.frames.ReadLine()
		if err != nil {
			p.t.Fatalf("frame pipe ended waiting for replies to %v: %v; stderr:\n%s", ids, err, p.stderr.String())
		}
		frame, err := nodeproto.ParseFrame(line)
		if err != nil {
			p.t.Fatalf("protocol_error: %v: %s", err, line)
		}
		var obj map[string]any
		if err := json.Unmarshal(line, &obj); err != nil {
			p.t.Fatal(err)
		}
		p.transcript = append(p.transcript, "<- "+normalize(p.t, obj))
		if frame.Kind == nodeproto.KindLog {
			p.validate(p.sc.record, "", obj, "log record")
			if ev, _ := obj["event"].(string); slices.Contains(hostEvents, ev) {
				p.t.Errorf("log record carries event %s, which only the host writes: %s", ev, line)
			}
			logs = append(logs, obj)
			continue
		}
		op, err := p.tracker.Resolve(frame.Reply)
		if err != nil {
			p.t.Fatalf("protocol_error: %v", err)
		}
		p.validate(p.sc.proto, "reply", obj, "reply")
		if result, ok := obj["result"].(map[string]any); ok {
			p.validateResult(op, result)
		}
		replies[frame.Reply.ID] = obj
	}
	return replies, logs
}

func (p *proc) validateResult(op nodeproto.Op, result map[string]any) {
	switch op {
	case nodeproto.OpDescribe:
		p.validate(p.sc.doc, "", result, "describe result")
	case nodeproto.OpPlan:
		p.validate(p.sc.doc, "plan", result, "plan result")
	case nodeproto.OpRunNode, nodeproto.OpRunStep:
		p.validate(p.sc.proto, "runResult", result, string(op)+" result")
	case nodeproto.OpEval:
		p.validate(p.sc.proto, "evalResult", result, "eval result")
		if nodes, ok := result["value"].([]any); ok {
			for _, n := range nodes {
				p.validate(p.sc.doc, "node", n, "generated node")
			}
		}
	}
}

func (p *proc) validate(s *Schema, def string, v any, at string) {
	p.t.Helper()
	d, ok := s.Def(def)
	if !ok {
		p.t.Fatalf("schema has no $defs/%s", def)
	}
	if errs := s.Validate(d, v, at); len(errs) > 0 {
		p.t.Errorf("does not match docs/schemas: %s", strings.Join(errs, "; "))
	}
}

func (p *proc) check(send, reply map[string]any, logs []map[string]any, want expect) {
	p.t.Helper()
	at := fmt.Sprintf("%v %v", send["op"], send)
	errObj, _ := reply["error"].(map[string]any)
	if want.Code != "" {
		msg, _ := errObj["message"].(string)
		if reply["ok"] != false || errObj["code"] != want.Code || !strings.Contains(msg, want.MessageContains) {
			p.t.Errorf("%s: reply %v, want a failed reply with code %s and a message containing %q", at, reply, want.Code, want.MessageContains)
		}
		return
	}
	result, _ := reply["result"].(map[string]any)
	if reply["ok"] != true {
		p.t.Errorf("%s: reply %v, want ok", at, reply)
		return
	}
	if want.Result != nil {
		if err := subset(want.Result, result, "result"); err != nil {
			p.t.Errorf("%s: %v; got %v", at, err, result)
		}
	}
	for _, key := range want.Absent {
		if _, has := result[key]; has {
			p.t.Errorf("%s: result carries %q, want it absent: %v", at, key, result)
		}
	}
	if send["op"] == "plan" {
		p.checkPlan(result, want.PlanNodes)
	}
	for _, rec := range want.Logs {
		requireLog(p.t, logs, rec)
	}
}

// safety: spec_hash and requires belong to the host, so an SDK that writes
// either is out of contract even when the plan is otherwise right.
func (p *proc) checkPlan(doc, want map[string]any) {
	p.plan = doc
	nodes, _ := doc["nodes"].([]any)
	got := map[string]any{}
	for _, n := range nodes {
		node, _ := n.(map[string]any)
		id, _ := node["id"].(string)
		got[id] = node
		for _, hostOwned := range []string{"spec_hash", "requires"} {
			if _, has := node[hostOwned]; has {
				p.t.Errorf("plan node %s carries %s, which only the host fills", id, hostOwned)
			}
		}
	}
	if want == nil {
		return
	}
	if err := subset(want, got, "plan nodes"); err != nil || len(got) != len(want) {
		p.t.Errorf("plan nodes by id = %v, want exactly the nodes %v (%v)", got, want, err)
	}
}

func (p *proc) closureID(ref map[string]any) string {
	p.t.Helper()
	nodes, _ := p.plan["nodes"].([]any)
	for _, n := range nodes {
		node, _ := n.(map[string]any)
		if node["id"] != ref["node"] {
			continue
		}
		holder := node
		if ref["step"] != nil {
			work, _ := node["work"].(map[string]any)
			steps, _ := work["steps"].([]any)
			holder = nil
			for _, s := range steps {
				if s, _ := s.(map[string]any); s["id"] == ref["step"] {
					holder = s
				}
			}
		}
		closures, _ := holder["closures"].(map[string]any)
		switch ids := closures[ref["kind"].(string)].(type) {
		case string:
			return ids
		case []any:
			i, _ := ref["index"].(float64)
			if int(i) < len(ids) {
				id, _ := ids[int(i)].(string)
				return id
			}
		}
	}
	p.t.Fatalf("the plan lists no closure %v", ref)
	return ""
}

// safety: end of input must end the process with status 0 and no frame after
// the last reply, which is how a host stops every process it starts.
func (p *proc) end() {
	p.t.Helper()
	if err := p.req.Close(); err != nil {
		p.t.Fatal(err)
	}
	if line, err := p.frames.ReadLine(); !errors.Is(err, io.EOF) {
		p.t.Errorf("frame after end of input: %q, %v", line, err)
	}
	if err := p.cmd.Wait(); err != nil {
		p.t.Errorf("exit after end of input: %v; stderr:\n%s", err, p.stderr.String())
	}
}

func requireLog(t *testing.T, logs []map[string]any, want map[string]any) {
	t.Helper()
	for _, rec := range logs {
		if subset(want, rec, "record") == nil {
			return
		}
	}
	t.Errorf("no log record matches %v among %v", want, logs)
}

// safety: objects match on the keys want names, so an SDK may add optional
// fields, while arrays and scalars match exactly.
func subset(want, got any, at string) error {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: %v is not an object", at, got)
		}
		for k, wv := range w {
			gv, has := g[k]
			if !has {
				return fmt.Errorf("%s.%s is missing", at, k)
			}
			if err := subset(wv, gv, at+"."+k); err != nil {
				return err
			}
		}
		return nil
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return fmt.Errorf("%s: %v, want %d items", at, got, len(w))
		}
		for i := range w {
			if err := subset(w[i], g[i], fmt.Sprintf("%s[%d]", at, i)); err != nil {
				return err
			}
		}
		return nil
	}
	if !reflect.DeepEqual(want, got) {
		return fmt.Errorf("%s = %v, want %v", at, got, want)
	}
	return nil
}

// safety: timestamps, measured latencies and the SDK's build version differ
// on every run, so the golden transcripts hold placeholders for them.
func normalize(t *testing.T, v map[string]any) string {
	t.Helper()
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, sub := range v {
				switch _, isStr := sub.(string); {
				case k == "ts" && isStr:
					v[k] = "<ts>"
				case k == "duration_ms" || k == "cancellation_latency_ms":
					v[k] = 0
				case k == "sdk":
					if sdk, ok := sub.(map[string]any); ok && sdk["version"] != nil {
						sdk["version"] = "<version>"
					}
				default:
					walk(sub)
				}
			}
		case []any:
			for _, sub := range v {
				walk(sub)
			}
		}
	}
	walk(v)
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(out.String(), "\n")
}

func golden(t *testing.T, path string, lines []string) {
	t.Helper()
	got := strings.Join(lines, "\n") + "\n"
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v; run with -update to record it", err)
	}
	if string(want) != got {
		t.Errorf("transcript differs from %s; run with -update after checking the change:\n--- want\n%s--- got\n%s", path, want, got)
	}
}
